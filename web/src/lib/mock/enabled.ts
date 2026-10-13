// Mock switch. Build-time public variable injection (NEXT_PUBLIC_ prefix to be browser-readable).
// Vercel upset NEXT_PUBLIC_MOCK=1 i.e.whole sitego mock,Noneneedbackend.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
