import React from 'react';
import {flushSync} from 'react-dom';
import {createRoot} from 'react-dom/client';
import {Button, Description, Dropdown, Label} from '@heroui/react';
import {Icon} from './sidebar';

// The session header's "More actions" menu. The controller passes the session context and the items in groups
// (links and panels, then session actions) and performs every action; the component owns the menu and its keyboard.
export interface TopbarMenuItem {
 id: string; icon: string; label: string; description?: string;
 href?: string; danger?: boolean; data?: Record<string, string>;
}
export interface TopbarMenuProps {
 icons: Record<string, string>; label: string;
 context: {project: string; session: string; branch?: string};
 groups: TopbarMenuItem[][];
 onAction(id: string): void;
}

const dataAttrs = (data?: Record<string, string>) => Object.fromEntries(Object.entries(data || {}).map(([key, value]) => [`data-${key}`, value]));

export function TopbarMenu(p: TopbarMenuProps) {
 const groups = p.groups.filter((group) => group.length);
 return <Dropdown>
  <Button aria-label={p.label} className="btn ghost sm icon-only topbar-menu-trigger" data-topbar-menu="" isIconOnly size="sm" variant="ghost">
   <Icon d={p.icons.more}/>
  </Button>
  <Dropdown.Popover className="side-pro-popover topbar-menu topbar-menu-pro" placement="bottom end">
   <div className="topbar-menu-context">
    <strong>{p.context.project}</strong>
    <span>{p.context.session}</span>
    {p.context.branch ? <span className="mono muted">{p.context.branch}</span> : null}
   </div>
   <Dropdown.Menu aria-label={p.label} onAction={(key) => p.onAction(String(key))}>
    {groups.map((group, index) => <Dropdown.Section key={index} className={index ? 'topbar-menu-section' : undefined}>
     {group.map((item) => <Dropdown.Item key={item.id} id={item.id} textValue={item.label} variant={item.danger ? 'danger' : undefined}
      href={item.href} target={item.href ? '_blank' : undefined} rel={item.href ? 'noopener noreferrer' : undefined} {...dataAttrs(item.data)}>
      <Icon d={p.icons[item.icon]} className={`side-pro-item-icon${item.danger ? ' danger' : ''}`}/>
      {item.description
       ? <div className="side-pro-item-text"><Label>{item.label}</Label><Description>{item.description}</Description></div>
       : <Label>{item.label}</Label>}
     </Dropdown.Item>)}
    </Dropdown.Section>)}
   </Dropdown.Menu>
  </Dropdown.Popover>
 </Dropdown>;
}

// The header is rebuilt on every render; the controller re-attaches this host each time. The first render is
// synchronous so the header never shows a frame without its menu button.
export function mountTopbarMenu(container: HTMLElement, initial: TopbarMenuProps) {
 const root = createRoot(container);
 let disposed = false;
 container.dataset.proMounted = 'true';
 const update = (props: TopbarMenuProps) => {if (!disposed) root.render(<TopbarMenu {...props}/>);};
 flushSync(() => update(initial));
 return {update, unmount: () => {if (!disposed) {disposed = true; root.unmount(); delete container.dataset.proMounted;}}};
}
