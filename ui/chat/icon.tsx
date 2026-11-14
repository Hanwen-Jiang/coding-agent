import React from 'react';
import {ICONS, iconPath} from '../../internal/web/static/icons.js';

export type IconName = keyof typeof ICONS;

// The application's icons (icons.js: Gravity UI on a 16x16 grid, the set HeroUI's own components use).
// Pass a name, or d: a value the controller took from the same table.
export function Icon({d, name, size = 'sm', className = ''}: {d?: string; name?: IconName; size?: string; className?: string}) {
 const glyph = iconPath(name ? ICONS[name] : d);
 return <svg viewBox="0 0 16 16" className={`icon gi ${size} ${className}`.trim()} aria-hidden="true"><path d={glyph.d} fillRule={glyph.rule === 'nonzero' ? 'nonzero' : undefined}/></svg>;
}
