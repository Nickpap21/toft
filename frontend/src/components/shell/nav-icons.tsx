import type { SVGProps } from "react";
import { TOFT_MARK_PATH } from "./toft-mark";

// The navigation's icons, drawn as the boards draw them (Sidebar.dc.html):
// 24-unit paths at stroke 1.8, square-cornered where lucide rounds.

export type NavIcon = (props: SVGProps<SVGSVGElement>) => React.JSX.Element;

const icon = (d: string): NavIcon =>
  function Icon(props) {
    return (
      <svg
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={1.8}
        strokeLinecap="round"
        strokeLinejoin="round"
        {...props}
      >
        <path d={d} />
      </svg>
    );
  };

export const HomeIcon = icon(
  "M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z",
);
export const InboxIcon = icon(
  "M22 12h-6l-2 3h-4l-2-3H2M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z",
);
export const IssuesIcon = icon(
  "M3 12a9 9 0 1 0 18 0a9 9 0 1 0-18 0M11 12a1 1 0 1 0 2 0a1 1 0 1 0-2 0",
);
export const CyclesIcon = icon("M21 12a9 9 0 0 1-15.5 6.2M3 12A9 9 0 0 1 18.5 5.8M18 2v4h-4M6 22v-4h4");
export const DocsIcon = icon(
  "M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2zM22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7z",
);
export const FolderIcon = icon(
  "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z",
);
export const ServersIcon = icon(
  "M3 4h18v7H3zM3 13h18v7H3zM7 7.5h.01M7 16.5h.01",
);
export const GlobeIcon = icon(
  "M2 12a10 10 0 1 0 20 0a10 10 0 1 0-20 0M12 2a14.5 14.5 0 0 0 0 20a14.5 14.5 0 0 0 0-20M2 12h20",
);
export const LayersIcon = icon("m12 2 10 5-10 5L2 7zM2 17l10 5 10-5M2 12l10 5 10-5");
export const CodeIcon = icon("m16 18 6-6-6-6M8 6l-6 6 6 6");
export const BoxesIcon = icon(
  "M7.5 4.27 12 2l4.5 2.27v5.06L12 11.6 7.5 9.33zM12 11.6v5.07M2.5 14.27 7 12l4.5 2.27v5.06L7 21.6l-4.5-2.27zM12.5 14.27 17 12l4.5 2.27v5.06L17 21.6l-4.5-2.27z",
);
export const ShieldIcon = icon("M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z");
export const SlidersIcon = icon(
  "M20 7h-9M14 17H5M14 17a3 3 0 1 0 6 0a3 3 0 1 0-6 0M4 7a3 3 0 1 0 6 0a3 3 0 1 0-6 0",
);
/** The Toft mark, for Instance: the panel itself (D-68). The logo's own
 *  geometry, so it reads as the panel and not a page. */
export function InstanceIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 256 256" {...props}>
      <path d={TOFT_MARK_PATH} fill="currentColor" fillRule="evenodd" />
    </svg>
  );
}
export const SearchIcon = icon(
  "M4 11a7 7 0 1 0 14 0a7 7 0 1 0-14 0M20 20l-3.5-3.5",
);
export const PlusIcon = icon("M12 5v14M5 12h14");
// The project sidebar's (D-136), from ReworkService.
export const GridIcon = icon("M3 3h7v9H3zM14 3h7v5h-7zM14 12h7v9h-7zM3 16h7v5H3z");
export const BranchIcon = icon("M6 3v12M18 9a3 3 0 1 0 0-6a3 3 0 1 0 0 6M6 21a3 3 0 1 0 0-6a3 3 0 1 0 0 6M18 9a9 9 0 0 1-9 9");
export const BackIcon = icon("m15 18-6-6 6-6");
export const UpDownIcon = icon("m7 15 5 5 5-5M7 9l5-5 5 5");
