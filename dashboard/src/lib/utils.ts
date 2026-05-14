import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";

/** shadcn `cn` helper — merge conditional class names with Tailwind precedence. */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
