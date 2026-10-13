package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Global settings key (exists in the settings key value table, no need to create a table).
const (
	// settingNotifyEnabled is the push master switch. On by default: It is used to stop bleeding with one click during the maintenance period.
	// Rather than the activation condition of the function - the real activation condition is "whether there is a distribution channel."
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the external access address that generates a link back to the vulnerability details.
	// (eg https://artex.example.com). Leave it blank to remove the link button in the message.
	// There is no reusable external address configuration in the project, so a new one is added here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the period in minutes for summary mode.
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the delivery engine's polling interval. 3 seconds is the upper limit of the real-time performance of this engine.
	// It is also the main source of delay between "vulnerability is logged" and "message reaches IM".
	notifyTick = 3 * time.Second
	// notifyLease is the lease duration when picking up delivery. Must be significantly greater than the worst-case elapsed time for a single delivery
	// (The HTTP client of the notify package times out for 15 seconds), otherwise the same line will be displayed twice.
	// dispatcher delivers simultaneously.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick limits the number of events dispatched per round to avoid when the channel is first enabled
	// Expand the entire historical backlog into delivery tasks at once.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default value for the aggregation period.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the upper limit of delivery per round when the channel does not set a current limit.
	// The meaning of existence is to prevent "a channel with unlimited traffic + sweeping out thousands of vulnerabilities at once"
	// A single cycle drags on for a long time.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is the maximum number of messages a single channel can deliver in each round.
	//
	// This upper limit is deduced from the **lease duration**: the lease is given to the bank when picking up (notifyLease = 3 minutes),
	// If the number of serial delivery items in one round is so large that at worst it takes longer than the lease, the leases for the next few items will expire before they are delivered.
	// It doesn't matter within a single process (Run is a single goroutine serial run, tick will not re-enter), but ** two
	// When a process is connected to a library, the peer will re-acquire the rows with expired leases and send them repeatedly, and will also
	// Attempts are double-incremented and will be judged as failed while the original process is still delivering.
	//
	// Value: 3 minutes lease / 30 seconds single timeout = 6 is **just full lease**, zero margin,
	// Cannot be taken; take 5 to allow a 30-second margin for a worst-case time of 150 seconds. This relationship is caused by
	// TestNotifyTickBudgetFitsWithinLease nailed - change notifyLease,
	// Either notifySendTimeout or this value will cause that assertion to fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the timeout for a single delivery. It also determines the value of the previous constant,
	// The two cannot be multiplied by more than notifyLease, see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the backoff sequence for failed retries, and the subscript is the number of attempts.
// The three opportunities (including the first) correspond to db.MaxNotifyAttempts, and both must be changed together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier is the delivery engine for vulnerability push.
//
// Runs as a standalone goroutine in parallel with the Scheduler (see server.New). Deliberately not reused
// Scheduler's tick: The real-time requirement of push (3 seconds) is different from the business rhythm of trigger.
// And the failures of the two are not related to each other - the stuck push should not affect the agent triggering.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu protects buckets. The number of channels is small, competition is low, and a mutex lock is enough.
	// It's not worth introducing a finer-grained structure to it.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a single-channel token bucket.
//
// The reason for using token buckets instead of "clear every minute" sliding windows is because the latter has terrible edge effects:
// At the end of the window, 20 messages are sent, and another 20 messages are sent in the next moment. For the platform, it is 40 messages in one second.
// Will be current-limited; the token bucket is replenished at a constant rate to naturally avoid such bursts.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loop until ctx ends. Started once by server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step runs for one round: new events are dispatched first, and then expired tasks are delivered.
//
// If any step fails, it will only be logged and the cycle will not be interrupted - the failure of the notification system must not be escalated to a process-level problem.
// Each tick is independent and will be naturally retried in the next round.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] Failed to dispatch event: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] Failed to read channel: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The measurement unit of the token bucket is the number of messages (equivalent to the number of HTTP requests), not the number of vulnerabilities.
		// In real-time mode, the two are the same (one vulnerability, one message); in summary mode, a whole batch of vulnerabilities are synthesized
		// One message, so only one token is consumed.
		//
		// In both modes, you first ask the token bucket, and then claim it according to the amount - the order cannot be reversed, otherwise it will be blocked by the current limit.
		// The delivery has exhausted the number of retries.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns the token consumption and batch size upper bound of the current round of the summary channel.
//
// The two return values are **two different dimensions**, which is why they are independent functions:
//
//   - tokens is the number of messages. A batch of vulnerabilities are combined into one message and one HTTP request, so it is always 1.
//     rate_per_min therefore still works for digest (up to this many aggregated messages per minute).
//   - claimLimit is the maximum number of vulnerabilities installed in this batch. It is only bounded by the upper memory bound and has nothing to do with the request budget.
//
// In order to make rate_per_min effective for digest, the request budget for each round was
// (notifyMaxSendsPerChannelPerTick, derived from the lease) is passed directly as the batch size.
// The consequence is that the channel with rate_per_min=20 only replenishes 1 token in the 3-second tick, so each summary
// The message only contains one vulnerability - digest degenerates into "real-time push with summary copy", and readers receive a series of
// "One new vulnerability has been added in the past 30 minutes", and db.MaxDigestBatchSize can never be reached.
//
// This symptom is not easy to find in end-to-end testing (existing use cases manually pass a large enough limit to
// stepDigest, bypassing the limit calculation in step), so the decision is included here because
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget Pinned directly.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime collects and delivers real-time tasks of a certain channel, with one vulnerability and one message.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] Failed to receive real-time delivery channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel type %q is not registered", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// Rendering failure is a local data problem and retrying won't make it better.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates the pending deliveries of a certain channel into one message and sends it out when the batch expires.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] Failed to judge summary batch channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] Failed to receive summary batch channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("Channel type %q is not registered", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Deliveries whose snapshots are broken and messages cannot be received must be explicitly failed. Otherwise they will remain
	// Except included, neither the message nor the failure list will be included - their status will be changed when the sending is successful.
	// Subsequent batch markings are missed and will always stop sending until the lease expires and is repeatedly claimed.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "The event snapshot cannot be parsed and this vulnerability cannot be rendered into a message."
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] Mark bad snapshot delivery failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] Skip %d deliveries that cannot be parsed by snapshot channel=%d", len(skipped), ch.ID)
	}
	// Only those that have entered the message are handed over to send: included[i] strictly corresponds to msg.Items[i],
	// Send relies on this correspondence to place "the first K items of channel returns" on the correct delivery line.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and transfers status according to the result.
//
// The same batch of deliveries (perhaps dozens in summary mode) share a sending result: either delivered or the entire batch is retried.
// Do not retry one by one - the summary message is one, and resending part of it will mess up the batch semantics.
//
// The only exception is **segmentation caused by the upper limit of channel length**: the channel return actually only contains the first K items.
// Then items K+1 and onwards must be retained for the next batch, instead of being marked successfully together. Otherwise it will be truncated
// Those vulnerabilities are neither in the message nor in the failure list, they have completely disappeared.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Set an upper limit for a single delivery to prevent one channel from being stuck and dragging down all the remaining channels in this round.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The number of items reported by the channel cannot exceed the number of deliveries; if this happens, it means that the rendering layer has made a miscalculation.
			// It's better to treat it all as delivered and write down the problem than to write a messy record.
			log.Printf("[notify] The number of delivered items in the channel report %d exceeds the number of deliveries %d channel=%s, and will be processed as all delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] Tag delivered failed channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message has reached the upper limit of the channel length: the remaining messages will be returned to the queue immediately and will be continued on the next tick.
			// Use DeferDeliveries instead of RescheduleDeliveries - this is not a failure,
			// The retry budget should not be consumed (it was already optimistic +1 when receiving it, and it will be reduced there).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("This message has reached the maximum length of the channel. Only the first %d messages will be delivered, and the rest will be reserved for the next batch.", delivered)); err != nil {
				log.Printf("[notify] Failed to queue for segment renewal channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither reported an error nor said how many messages were delivered. Treat as failure (step back),
		// This prevents this delivery from being collected repeatedly but never being marked.
		err = fmt.Errorf("The channel did not report the number of delivered items (delivered=%d)", delivered)
	}

	// Failure handling is determined item by item, rather than based on the maximum number of attempts in the entire batch.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts`, which is critical to death, but in batches
	// The number of attempts for each entry is not the same: an old delivery that has been retried twice (attempts=2) will be included in the same batch.
	// Drag the new delivery (attempts=1) into failed - the new vulnerability will be lost forever without even a single retry.
	// It is exactly the opposite of the original intention of "not letting the old industry drag down the new industry".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] Mark failed status error channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("Failed after %d retries: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] Mark failed status error channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Rearrange by delayed grouping: There are only 3 levels of backoff, and the number of groups is naturally very small. There is no need to send each item separately.
	// UPDATE (that would result in 500 round trips for a batch of 500 items).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] Rearrangement delivery failed channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] Delivery failed channel=%d kind=%s Permanent failure=%d Retry exhausted=%d Pending retry=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns those in all that are not in keep (compare by pointer identity).
// Used to find "failed" deliveries - they must be handled explicitly and cannot be left in a gray area.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt takes the channel implementation and parses its configuration.
// Returning ok=false means that the type is not registered, and the delivery should directly fail instead of retrying infinitely.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// When the configuration parsing fails, an empty map is given: the Validate of the channel itself will report "Which field is missing?"
		// That error is more user-friendly to fix than the JSON parsing error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single vulnerability message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch renders summary messages. Analyze snapshots one by one - if a single one is broken, just skip that one.
// Don't let it drag down the entire batch summary.
//
// The return value included has a strict one-to-one correspondence with msg.Items (i-th delivery ↔ i-th entry).
// This correspondence is a hard requirement: the caller determines the top K deliveries based on "the channel reports the first K items."
// Mark delivered. If bad snapshots are skipped here but the skipped deliveries are not excluded from included,
// The subscripts will be misaligned - bad items that should have failed will be marked as delivered, and good items will be mistakenly marked as not delivered.
// Broken ones are explicitly marked as failed by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// Bad snapshots do not carry messages, nor are they included - their disposal is the responsibility of the caller
			// (Explicitly mark the failure, rather than getting away with it as "delivered").
			log.Printf("[notify] Skipping unresolved snapshots in rollup batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("All %d deliveries in the summary batch cannot be parsed", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders the event snapshot into an item to be pushed, and parses the asset name and details back link.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// Failure to resolve the asset name should not prevent the push: not being able to read the name is much less serious than not receiving the notification.
		// There is just one line of assets missing in the message.
		log.Printf("[notify] Failed to parse asset name finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// For details page routing, see web/src/app/(main)/function/findings/detail/page.tsx.
		// It reads the vulnerability id from the query parameter id.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens takes up to **want** tokens from the channel token bucket and returns the actual number taken.
//
// One token = one message (one HTTP request). In real-time mode, the caller can send as many messages as he wants;
// In summary mode, only one message is sent for a whole batch of vulnerabilities, passing 1.
//
// The bucket capacity is the upper limit of the channel per minute and is replenished at a constant rate. ratePerMin<=0 means no current limit,
// Returns a finite but large enough value to prevent a single round of loops from being bogged down by an infinite backlog.
//
// The want upper limit is required: without it, the entire bucket can be emptied, and the caller itself has a per-round upper limit.
// The extra tokens cannot be used and disappear before the next replenishment - the saved burst capacity will never be reached.
// Even "there are no pending deliveries in this round" will be deducted.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Complemented by real time elapsed, the rate is ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a very small epsilon and then round: the number of tokens is accumulated by floating point, and it is filled up in two times
	// 0.5 + 0.5 may get 0.9999999999, direct int() will be truncated to 0——
	// No token can be taken out of a mathematically full bucket. 1e-9 is far smaller than a token and will not let go of the real amount owed.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the main switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the external address used for linking back, minus the trailing slash.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the summary interval, falling back to the default value when illegal or not configured.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses and delivers the snapshot of the corresponding event.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("Event snapshot for post %d is empty", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("Failed to parse event snapshot for delivery %d: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// The event type is subject to the event behavior, and the one in the snapshot may have been written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
