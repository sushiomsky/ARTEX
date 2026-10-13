import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Assist judging Close intent in drawer/box (Sheet/Dialog) onInteractOutside.
//
// Background: Radix popups inside a drawer (Select dropdown, DropdownMenu, Popover, etc.) portal to the drawer
// itself. Clicking overlay/outside with a popup open looks like dismissing it, so this pointerdown hits both Select and Sheet
// DismissableLayer samewhenhandle;Select firstOffand is  discrete event,React willsync flush,at is 
// rounds to the Sheet handler, where a popup's data-state flips to closed early — detecting 'now' whether a popup is open
// daybutnotreliable(measuredverify).
//
    true, // correctlyapproach:Radix   pointerdown listen in bubblestage;we in  capture stage(earlyatit)first
// "Whether any dialog is open right now"recorddowncome,onInteractOutside againreadthisrecordvaluecomedecidewhetherAllowOff.
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
