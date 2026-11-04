import type { ReactElement, SVGProps } from "react";

// One glyph per concept; a glyph never stands for two things. The key glyph is
// reserved for inference keys people use; provider access never uses it.
type IconName =
	| "pool"
	| "models"
	| "people"
	| "activity"
	| "system"
	| "plus"
	| "close"
	| "chevron"
	| "chevronRight"
	| "check"
	| "search"
	| "link"
	| "lock"
	| "alert"
	| "upload"
	| "external"
	| "copy"
	| "refresh"
	| "more"
	| "trash"
	| "signin"
	| "key"
	| "file"
	| "import"
	| "sun"
	| "moon"
	| "auto"
	| "signout"
	| "back"
	| "info";

const paths: Record<IconName, ReactElement> = {
	pool: (
		<>
			<rect x="3" y="3" width="10" height="11" rx="1.5" />
			<rect x="15" y="3" width="6" height="6" rx="1.5" />
			<rect x="15" y="11" width="6" height="10" rx="1.5" />
			<rect x="3" y="16" width="10" height="5" rx="1.5" />
		</>
	),
	models: (
		<>
			<path d="M12 3 3 7.5l9 4.5 9-4.5L12 3Z" />
			<path d="m3 12 9 4.5 9-4.5" />
			<path d="m3 16.5 9 4.5 9-4.5" />
		</>
	),
	people: (
		<>
			<circle cx="9" cy="8" r="3.5" />
			<path d="M2.5 20c.8-3.6 3.4-5.5 6.5-5.5s5.7 1.9 6.5 5.5" />
			<path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14.8c1.8.7 3 2.4 3.5 5.2" />
		</>
	),
	activity: <path d="M2 12h4l2.5-7 5 14 2.5-7h6" />,
	system: (
		<>
			<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1" />
			<circle cx="15" cy="6" r="2" />
			<circle cx="9" cy="12" r="2" />
			<circle cx="17" cy="18" r="2" />
		</>
	),
	plus: <path d="M12 5v14M5 12h14" />,
	close: <path d="M6 6l12 12M18 6 6 18" />,
	chevron: <path d="m6 9 6 6 6-6" />,
	chevronRight: <path d="m9 6 6 6-6 6" />,
	check: <path d="m5 12.5 4.5 4.5L19 7" />,
	search: (
		<>
			<circle cx="11" cy="11" r="6.5" />
			<path d="m20 20-4.2-4.2" />
		</>
	),
	link: (
		<>
			<path d="M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1" />
			<path d="M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1" />
		</>
	),
	lock: (
		<>
			<rect x="5" y="10.5" width="14" height="10" rx="2" />
			<path d="M8 10.5V7.5a4 4 0 0 1 8 0v3" />
		</>
	),
	alert: (
		<>
			<path d="M12 3.5 2.5 20h19L12 3.5Z" />
			<path d="M12 10v4.5M12 17.2v.1" />
		</>
	),
	upload: (
		<>
			<path d="M12 15V4M7.5 8.5 12 4l4.5 4.5" />
			<path d="M4 15v3.5A1.5 1.5 0 0 0 5.5 20h13a1.5 1.5 0 0 0 1.5-1.5V15" />
		</>
	),
	external: (
		<>
			<path d="M14 4h6v6M20 4l-9 9" />
			<path d="M18 14v4.5a1.5 1.5 0 0 1-1.5 1.5h-11A1.5 1.5 0 0 1 4 18.5v-11A1.5 1.5 0 0 1 5.5 6H10" />
		</>
	),
	copy: (
		<>
			<rect x="8" y="8" width="12" height="12" rx="2" />
			<path d="M16 8V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v9A1.5 1.5 0 0 0 5.5 16H8" />
		</>
	),
	refresh: (
		<>
			<path d="M20 11a8 8 0 0 0-14.3-4.8L4 8" />
			<path d="M4 4v4h4M4 13a8 8 0 0 0 14.3 4.8L20 16" />
			<path d="M20 20v-4h-4" />
		</>
	),
	more: (
		<>
			<circle cx="5.5" cy="12" r="1.2" />
			<circle cx="12" cy="12" r="1.2" />
			<circle cx="18.5" cy="12" r="1.2" />
		</>
	),
	trash: (
		<>
			<path d="M4 7h16M10 11v6M14 11v6" />
			<path d="M6 7l1 12.5A1.5 1.5 0 0 0 8.5 21h7a1.5 1.5 0 0 0 1.5-1.5L18 7M9 7V4.5A1 1 0 0 1 10 3.5h4a1 1 0 0 1 1 1V7" />
		</>
	),
	signin: (
		<>
			<path d="M14 4h4.5A1.5 1.5 0 0 1 20 5.5v13a1.5 1.5 0 0 1-1.5 1.5H14" />
			<path d="M3.5 12H14M10 8l4 4-4 4" />
		</>
	),
	key: (
		<>
			<circle cx="8" cy="15" r="4.5" />
			<path d="m11.2 11.8 8.3-8.3M16.5 6.5l2.5 2.5M14 9l2 2" />
		</>
	),
	file: (
		<>
			<path d="M13.5 3H6.5A1.5 1.5 0 0 0 5 4.5v15A1.5 1.5 0 0 0 6.5 21h11a1.5 1.5 0 0 0 1.5-1.5V8.5L13.5 3Z" />
			<path d="M13.5 3v5.5H19" />
		</>
	),
	import: (
		<>
			<path d="M12 3v11M7.5 9.5 12 14l4.5-4.5" />
			<path d="M4 17.5h16M4 21h16" />
		</>
	),
	sun: (
		<>
			<circle cx="12" cy="12" r="4" />
			<path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4" />
		</>
	),
	moon: <path d="M19.5 14.5A8 8 0 0 1 9.5 4.5a8 8 0 1 0 10 10Z" />,
	auto: (
		<>
			<circle cx="12" cy="12" r="8.5" />
			<path d="M12 3.5v17a8.5 8.5 0 0 0 0-17Z" fill="currentColor" stroke="none" />
		</>
	),
	signout: (
		<>
			<path d="M10 20H5.5A1.5 1.5 0 0 1 4 18.5v-13A1.5 1.5 0 0 1 5.5 4H10" />
			<path d="M20.5 12H10M16.5 8l4 4-4 4" />
		</>
	),
	back: <path d="M15 6 9 12l6 6" />,
	info: (
		<>
			<circle cx="12" cy="12" r="8.5" />
			<path d="M12 11v5.5M12 7.8v.1" />
		</>
	),
};

export function Icon({ name, size = 18, ...rest }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
	return (
		<svg
			width={size}
			height={size}
			viewBox="0 0 24 24"
			fill="none"
			stroke="currentColor"
			strokeWidth={1.6}
			strokeLinecap="round"
			strokeLinejoin="round"
			aria-hidden="true"
			focusable="false"
			{...rest}
		>
			{paths[name]}
		</svg>
	);
}

export type { IconName };
