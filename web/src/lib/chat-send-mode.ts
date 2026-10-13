"use client";

import * as React from "react";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// Session input send/switch-row keybinding. Pure frontend preference: localStorage only, never persisted, never synced with the account,
// so switching browsers needs a re-set. See issue #39 — 0.3.2 changed Ctrl+Enter-send to Enter-send,
// with the old keybinding kept here as a fallback option.
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

export const CHAT_SEND_MODE_OPTIONS: { value: ChatSendMode; label: string }[] = [
  { value: "enter", label: "Enter to send, Shift+Enter for newline" },
  { value: "ctrl-enter", label: "Ctrl+Enter to send, Enter for newline" },
];

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// samelabelpageinside subscribepersonset.localStorage   storage eventonly in 'other'labelpagetrigger,
// Changing it in settings must emit to notify inputs on the same page, otherwise a refresh is needed to take effect.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// Returns string char count. Object.is compares by value, keeping useSyncExternalStore out of loops.
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// Serviceendno localStorage,firstrenderDefaultvalue,hydrate after getSnapshot correct againexact.
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey judgeonceBy keywhethershouldsend.
// isComposing / keyCode 229 marks CJK input-method composition; must be allowed, otherwise Enter during candidate selection mis-sends.
// Enter pattern excludes Shift only, keeping per-character 0.3.2 behavior consistent — settings users feel no change.
// ctrl-enter Patternsamewhenaccept Ctrl and Cmd(macOS).
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
