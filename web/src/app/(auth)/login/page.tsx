"use client";

import { useEffect, useRef, useState } from "react";

import { useRouter } from "next/navigation";

import { AlertTriangle, ShieldCheck } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogClose, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import { auth } from "@/lib/auth";
import { t } from "@/lib/translations";

function getLocale(): 'en' | 'es' | 'fr' | 'zh' {
  if (typeof window !== 'undefined') {
    const lang = navigator.language.slice(0, 2);
    if (['en', 'es', 'fr', 'zh'].includes(lang)) return lang as 'en' | 'es' | 'fr' | 'zh';
  }
  return 'en';
}

export default function LoginPage() {
  const router = useRouter();
  const locale = getLocale();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [checking, setChecking] = useState(true);
  const [agreed, setAgreed] = useState(false);
  const [termsOpen, setTermsOpen] = useState(false);
  const [readToEnd, setReadToEnd] = useState(false);
  const termsBodyRef = useRef<HTMLDivElement>(null);
  // Agree is clickable only after scrolling the terms to the bottom (including when everything fits without scrolling).
  function handleTermsScroll() {
    const el = termsBodyRef.current;
    if (!el) return;
    if (el.scrollTop + el.clientHeight >= el.scrollHeight - 8) setReadToEnd(true);
  }

  useEffect(() => {
    if (!termsOpen) return;
    // Reset on open, including when the content fits on one screen and can never scroll.
    setReadToEnd(false);
    const el = termsBodyRef.current;
    if (el && el.scrollHeight <= el.clientHeight + 8) setReadToEnd(true);
  }, [termsOpen]);

  useEffect(() => {
    // Already signed in: go straight to the main UI (no middleware does this redirect in static export).
    const token = auth.getToken();
    if (token) {
      // localStorage may still hold credentials while the cookie is gone. Sync first, then make a fresh request,
      // so the server guard or route cache does not bounce the redirect back to the still-checking login page.
      auth.setToken(token);
      window.location.replace("/function/tasks");
      return;
    }
    api
      .authStatus()
      .then(({ initialized }) => {
        if (!initialized) router.replace("/setup");
      })
      .catch(() => setError("Cannot connect to the backend service"))
      .finally(() => setChecking(false));
  }, [router]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!agreed) {
                setError(t('errorAgreeFirst', locale));
      return;
    }
    setLoading(true);
    setError("");
    try {
      const { token } = await api.login("ARTEX", password);
      auth.setToken(token);
      window.location.replace("/function/tasks");
    } catch {
      setError("Invalid username or password");
    } finally {
      setLoading(false);
    }
  }

  if (checking) {
    return (
      <div role="status" className="flex min-h-dvh items-center justify-center text-muted-foreground">
        {t('checkingLogin', locale)}
      </div>
    );
  }

  return (
    <div className="flex h-dvh">
      {/* Left panel */}
      <div className="hidden flex-col items-center justify-center bg-primary p-12 text-center lg:flex lg:w-1/3">
        <div className="relative flex items-center justify-center">
          <div className="absolute size-80 rounded-full border border-primary-foreground/10" />
          <div className="absolute size-60 rounded-full border border-primary-foreground/15" />
          <div className="absolute size-40 rounded-full border border-primary-foreground/20" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src="/logo.png" alt="ARTEX" width={160} height={160} className="relative brightness-0 invert" />
        </div>
      </div>

      {/* Right panel */}
      <div className="flex w-full items-center justify-center bg-background p-8 lg:w-2/3">
        <div className="w-full max-w-md space-y-10 py-24 lg:py-32">
          <div className="space-y-4 text-center">
            <h2 className="text-2xl font-medium tracking-tight">Sign in</h2>
            <p className="mx-auto max-w-xl text-muted-foreground">{t('welcomeBack', locale)}</p>
          </div>
          <form onSubmit={handleSubmit} className="flex flex-col gap-4">
            <div className="space-y-1.5">
              <Label htmlFor="username">Username</Label>
              <Input id="username" value="ARTEX" readOnly className="bg-muted text-muted-foreground" />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder={t('passwordPlaceholder', locale)}
                autoFocus
                autoComplete="current-password"
              />
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="agree-terms"
                checked={agreed}
                onCheckedChange={(v) => setAgreed(v === true)}
                className="mt-0.5"
              />
              <Label htmlFor="agree-terms" className="text-sm font-normal leading-relaxed text-muted-foreground">
                I have read and agree to the
                <button
                  type="button"
                  onClick={() => setTermsOpen(true)}
                  className="mx-0.5 font-medium text-primary underline-offset-4 hover:underline"
                >
                  {"<Usage Notice>"}
                </button>
              </Label>
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={loading || !password || !agreed}>
              {loading ? "Signing in..." : "Sign in"}
            </Button>
          </form>
        </div>
      </div>

      <Dialog open={termsOpen} onOpenChange={setTermsOpen}>
        <DialogContent className="gap-0 p-0 sm:max-w-2xl">
          <DialogHeader className="flex-row items-center gap-3 border-b px-6 py-4">
            <div className="flex size-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <ShieldCheck className="size-5" />
            </div>
            <div className="space-y-0.5">
              <DialogTitle className="text-base">{t('termsTitle', locale)}</DialogTitle>
              <p className="text-xs text-muted-foreground">
                {t('termsVersion', locale)}
              </p>
            </div>
          </DialogHeader>

          <div
            ref={termsBodyRef}
            onScroll={handleTermsScroll}
            className="max-h-[60vh] space-y-5 overflow-y-auto px-6 py-5 text-sm leading-relaxed text-muted-foreground"
          >
            <p className="rounded-lg border bg-muted/40 p-3 text-foreground/80">
              This {"<Usage Notice and Disclaimer>"} (the "Statement") is the agreement between you and the ARTEX
              project authors and contributors governing your use of this software. Please read it carefully before use
              and make sure you understand every clause, especially the disclaimers, liability limits, and prohibitions
              marked in bold or colored blocks.
              <span className="font-medium text-foreground">
                {" "}
                Once you download, install, access, or otherwise use this software, you are deemed to have read,
                understood, and agreed to all of the constraints in this Statement.
              </span>
            </p>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  1
                </span>
                Article 1 · Definitions and open-source license
              </h4>
              <p className="pl-7">
                This software (ARTEX) is open-source software released under the GNU Affero General Public License
                v3.0 (AGPL-3.0). You may freely use, copy, modify, and distribute it under that license; however, any
                derivative work (including online services offered to third parties over a network) must likewise be
                released under AGPL-3.0 with the corresponding complete source code made available to users. The full
                AGPL-3.0 terms accompanying the LICENSE file shall prevail.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  2
                </span>
                Article 2 · Authorized scope of use
              </h4>
              <p className="pl-7">
                This software is for personal study, code research, discussion of security techniques, and technical
                verification in a self-hosted, isolated local environment only — i.e. non-offensive, non-destructive
                purposes such as learning, academic research, and code review. Except as expressly permitted in this
                article, you must not use this software for any other purpose.
              </p>
            </section>

            <section className="space-y-2">
              <h4 className="flex items-center gap-2 font-medium text-destructive">
                <span className="flex size-5 items-center justify-center rounded-md bg-destructive/10 text-xs font-semibold text-destructive">
                  3
                </span>
                <AlertTriangle className="size-4" />
                Article 3 · Prohibited conduct
              </h4>
              <ul className="ml-7 list-decimal space-y-1.5 rounded-lg border border-destructive/20 bg-destructive/5 p-3 pl-8 text-foreground/80 marker:text-destructive/70">
                <li>
                  Never scan, probe, exploit, or attack any website, online service, or networked system owned by
                  others or third parties (regardless of whether you claim authorization, and including your own
                  assets);
                </li>
                <li>Never use this software for any real penetration testing, adversary simulation, red/blue-team exercises, or production environments;</li>
                <li>Never use this software for unlawful intrusion, data theft, extortion, denial of service (DoS/DDoS), or any destructive or criminal activity;</li>
                <li>Never remove, alter, or circumvent any copyright, license, or security notice in this software or its output;</li>
                <li>Never engage in any conduct violating the laws, regulations, or regulatory requirements of your country or region.</li>
              </ul>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  4
                </span>
                Article 4 · Intellectual property
              </h4>
              <p className="pl-7">
                Copyright and related intellectual property in this software belong to the project authors and
                contributors, who grant you the corresponding rights within the scope of AGPL-3.0. Except for the
                rights expressly granted by that license, this Statement grants you no other rights, express or
                implied.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  5
                </span>
                Article 5 · Data and privacy
              </h4>
              <p className="pl-7">
                This software is self-hostable open-source software. The authors operate no centralized service and
                collect or upload none of your usage data. All data you generate, process, or come into contact with
                while using it stays under your own control, and you are responsible for its legality and security;
                you alone bear any consequences of improper data handling.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  6
                </span>
                Article 6 · Compliance and liability
              </h4>
              <p className="pl-7">
                You must comply with all applicable laws and regulations of your country or region on network security,
                data security, personal-information protection, and computer crime (in mainland China including without
                limitation the Cybersecurity Law, the Data Security Law, the Personal Information Protection Law, and
                related judicial interpretations).
                <span className="font-medium text-foreground">
                  {" "}
                  You alone and independently bear all legal liability and consequences arising from your violation of
                  the above laws or of this Statement; the authors and contributors of this software bear none of it.
                </span>
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  7
                </span>
                Article 7 · Disclaimer and limitation of liability
              </h4>
              <p className="pl-7">
                This software is provided "as is" and "as available", without any express or implied warranty,
                including without limitation warranties of merchantability, fitness for a particular purpose, accuracy,
                and non-infringement. To the maximum extent permitted by applicable law, the authors and contributors
                are not liable for any direct, indirect, incidental, special, or consequential loss arising from your
                use or misuse of this software (whether or not used properly), including without limitation data loss,
                system damage, business interruption, lost profits, or legal disputes.
              </p>
            </section>

            <section className="space-y-1.5">
              <h4 className="flex items-center gap-2 font-medium text-foreground">
                <span className="flex size-5 items-center justify-center rounded-md bg-muted text-xs font-semibold text-muted-foreground">
                  8
                </span>
                Article 8 · Changes and final interpretation
              </h4>
              <p className="pl-7">
                The authors may update this Statement from time to time as laws or the project evolve; updated versions
                ship with project releases and take effect upon publication, and your continued use of this software
                constitutes acceptance of the revised terms. Within the limits of the law, the project authors hold the
                final interpretation of this Statement. If any clause is found invalid, the remaining clauses stay in
                force.
              </p>
            </section>
          </div>

          <DialogFooter className="mx-0 mb-0 flex-col items-stretch gap-2 rounded-b-xl px-6 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-xs text-muted-foreground">
              {readToEnd ? "You have viewed all terms" : "Please scroll the terms to the bottom before confirming"}
            </p>
            <DialogClose asChild>
              <Button
                type="button"
                disabled={!readToEnd}
                onClick={() => {
                  setAgreed(true);
                  setError("");
                }}
              >
                I have read and agree to all terms
              </Button>
            </DialogClose>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
