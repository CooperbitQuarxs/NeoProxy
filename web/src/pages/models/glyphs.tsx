import type { SVGProps } from "react";

// Glyphs only this page needs. Each stands for one thing: reordering stages.
const paths = {
	grip: (
		<>
			<circle cx="9" cy="6" r="1.2" />
			<circle cx="15" cy="6" r="1.2" />
			<circle cx="9" cy="12" r="1.2" />
			<circle cx="15" cy="12" r="1.2" />
			<circle cx="9" cy="18" r="1.2" />
			<circle cx="15" cy="18" r="1.2" />
		</>
	),
	up: <path d="m6 15 6-6 6 6" />,
	down: <path d="m6 9 6 6 6-6" />,
};

export type GlyphName = keyof typeof paths;

export function Glyph({ name, size = 16, ...rest }: { name: GlyphName; size?: number } & SVGProps<SVGSVGElement>) {
	const filled = name === "grip";
	return (
		<svg
			width={size}
			height={size}
			viewBox="0 0 24 24"
			fill={filled ? "currentColor" : "none"}
			stroke={filled ? "none" : "currentColor"}
			strokeWidth={1.8}
			strokeLinecap="round"
			strokeLinejoin="round"
			aria-hidden="true"
			{...rest}
		>
			{paths[name]}
		</svg>
	);
}
