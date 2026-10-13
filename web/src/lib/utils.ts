import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Helper for judging Close intent in drawer/box (Sheet/Dialog) onInteractOutside.
//
// Background: Radix popups inside a drawer (Select dropdown, DropdownMenu, Popover, etc.) portal to the drawer
// itself. Clicking overlay/outside with a popup open also reaches the Sheet's DismissableLayer handler; Select closes
// first via a discrete event that React flushes synchronously, so by the time the Sheet handler runs, the popup's
// data-state has already flipped to closed — sampling openness at that point is unreliable (verified by measurement).
//
// Correct approach: listen for pointerdown in the capture stage (before Radix's bubble-stage handlers) and record
// "whether any dialog is open right now"; onInteractOutside then reads that recorded value to decide whether to allow close.
function isRadixOverlayOpenNow(): boolean {
  if (typeof document === "undefined") return false;
  return !!document.querySelector(
    [
      "[data-slot='select-trigger'][data-state='open']",
      "[data-slot='select-content'][data-state='open']",
      "[role='listbox'][data-state='open']",
      "[data-radix-popper-content-wrapper]",
      "[aria-expanded='true'][data-state='open']",
    ].join(","),
  );
}

let overlayOpenAtLastPointerDown = false;
if (typeof document !== "undefined") {
  document.addEventListener(
    "pointerdown",
    () => {
      overlayOpenAtLastPointerDown = isRadixOverlayOpenNow();
    },
    true, // capture:grab in  Radix bubblestage  pointerdown handledevicebeforerecord
  );
}

// radixOverlayWasOpenAtPointerDown returns "whether any Radix popup was open at the last pointerdown".
// Drawer/box rule: popup open + overlay click → close only the popup, not itself.
export function radixOverlayWasOpenAtPointerDown(): boolean {
  return overlayOpenAtLastPointerDown;
}

// Write text to the clipboard, returning success.
// background:navigator.clipboard only in securitycontext(HTTPS / localhost)available;through IP + HTTP
// accesswhenitfor undefined,at this pointdegradeto execCommand("copy").
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // continuegodegradeplan
    }
  }
  try {
    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.left = "-9999px";
    textarea.style.top = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    const ok = document.execCommand("copy");
    document.body.removeChild(textarea);
    return ok;
  } catch {
    return false;
  }
}

export const getInitials = (str: string): string => {
  if (typeof str !== "string" || !str.trim()) return "?";

  return (
    str
      .trim()
      .split(/\s+/)
      .filter(Boolean)
      .map((word) => word[0])
      .join("")
      .toUpperCase() || "?"
  );
};

export function formatCurrency(
  amount: number,
  opts?: {
    currency?: string;
    locale?: string;
    minimumFractionDigits?: number;
    maximumFractionDigits?: number;
    noDecimals?: boolean;
  },
) {
  const { currency = "USD", locale = "en-US", minimumFractionDigits, maximumFractionDigits, noDecimals } = opts ?? {};

  const formatOptions: Intl.NumberFormatOptions = {
    style: "currency",
    currency,
    minimumFractionDigits: noDecimals ? 0 : minimumFractionDigits,
    maximumFractionDigits: noDecimals ? 0 : maximumFractionDigits,
  };

  return new Intl.NumberFormat(locale, formatOptions).format(amount);
}
