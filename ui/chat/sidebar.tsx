import React, {useEffect, useMemo} from 'react';
import {Sidebar, useSidebar} from '@heroui-pro/react/sidebar';
import {Avatar, Description, Dropdown, Header, Kbd, Label, Separator} from '@heroui/react';

// The application controller (app.js) owns state, routing and transport. It passes serializable
// data plus callbacks; this component owns only presentation, keyboard behaviour and overlays.
export interface SideMark {viewBox?: string; paths?: Array<[string | null, string]>; cls?: string; text?: string; title?: string}
export interface SideNavItem {id: string; icon: string; label: string; title?: string; kbd?: string; count?: number; live?: boolean; current?: boolean}
export interface SideSession {id: string; title: string; when: string; live: boolean; current: boolean; mark?: SideMark | null}
export interface SideGroup {
 id: string; name: string; title: string; mark: SideMark; closed: boolean; live: boolean; drafting: boolean;
 sessions: SideSession[]; total: number; moreLabel?: string; addLabel: string;
}
export interface SideChoice {id: string; label: string}
export interface SidebarLabels {
 navigation: string; sessions: string; noWorkspaces: string; draft: string; noSessions: string; live: string;
 sessionMenu: string; rename: string; duplicate: string; duplicateDesc: string; remove: string;
 userMenu: string; settings: string; account: string; accountDesc: string; appearance: string; language: string; logout: string;
}
export interface SidebarProps {
 brand: string; collapsed: boolean; icons: Record<string, string>;
 nav: SideNavItem[]; groups: SideGroup[]; labels: SidebarLabels;
 user: {name: string; detail: string; avatarUrl?: string; initials: string};
 themes: SideChoice[]; theme: string; languages: SideChoice[]; language: string;
 onNav(id: string): void; onOpenSession(project: string, session: string): void; onDraft(project: string): void;
 onToggleGroup(project: string): void; onToggleAll(project: string): void;
 onSessionAction(action: string, project: string, session: string): void;
 onUserAction(action: string, value?: string): void; onCollapsedChange(collapsed: boolean): void;
}
export interface SidebarHandle {update(props: SidebarProps): void; unmount(): void; openMobile(): void}

export function Icon({d, size = 'sm', className = ''}: {d?: string; size?: string; className?: string}) {
 return <svg viewBox="0 0 24 24" className={`icon ${size} ${className}`} aria-hidden="true"><path d={d || ''}/></svg>;
}
export function Mark({mark, size = 'sm'}: {mark?: SideMark | null; size?: string}) {
 if (!mark) return null;
 if (mark.text) return <span className={`pv-badge ${size} side-mark-text`} title={mark.title}>{mark.text}</span>;
 return <svg viewBox={mark.viewBox || '0 0 24 24'} className={`icon ${size} ${mark.cls || ''}`} role={mark.title ? 'img' : undefined} aria-label={mark.title} aria-hidden={mark.title ? undefined : true}>
  {(mark.paths || []).map(([fill, d], i) => <path key={i} d={d} fill={fill || undefined}/>)}
 </svg>;
}
function Logo() {
 return <svg viewBox="0 0 24 24" className="icon logo bold" aria-hidden="true"><path d="M16.6 6.9A8 8 0 1 0 16.6 17.1" className="ring"/><path d="M20 9v6" className="cursor"/></svg>;
}
const only = (selection: unknown): string | undefined => {
 if (selection === 'all' || !selection) return undefined;
 for (const key of selection as Set<React.Key>) return String(key);
 return undefined;
};

function SidebarBody(props: SidebarProps & {prefix: string; compact: boolean}) {
 const {prefix, compact, icons, labels} = props;
 const expanded = useMemo(() => new Set(props.groups.filter((g) => !g.closed).map((g) => `${prefix}ws:${g.id}`)), [props.groups, prefix]);
 const strip = (key: React.Key) => String(key).slice(prefix.length);
 const navAction = (key: React.Key) => props.onNav(strip(key).slice('nav:'.length));
 const treeAction = (key: React.Key) => {
  const id = strip(key), at = id.indexOf(':'), kind = id.slice(0, at), rest = id.slice(at + 1);
  if (kind === 'ws') props.onToggleGroup(rest);
  else if (kind === 'draft' || kind === 'empty') props.onDraft(rest);
  else if (kind === 'more') props.onToggleAll(rest);
  else if (kind === 's') {const cut = rest.indexOf('/'); props.onOpenSession(rest.slice(0, cut), rest.slice(cut + 1));}
 };
 const expandedChange = (keys: Set<React.Key>) => {
  for (const g of props.groups) if (keys.has(`${prefix}ws:${g.id}`) === g.closed) props.onToggleGroup(g.id);
 };
 return <>
  <Sidebar.Header className="side-pro-head">
   <div className="side-pro-brand">
    <span className="mark" data-sidebar="label"><Logo/></span>
    <span className="side-pro-brand-name" data-sidebar="label">{props.brand}</span>
    {prefix === 'd-' ? <Sidebar.Trigger className="side-pro-trigger"><Icon d={icons.panelLeft}/></Sidebar.Trigger> : null}
   </div>
   <Sidebar.Menu aria-label={labels.navigation} className="side-pro-nav" onAction={navAction}>
    {props.nav.map((item) => <Sidebar.MenuItem key={item.id} id={`${prefix}nav:${item.id}`} textValue={item.label} isCurrent={!!item.current} tooltip={item.title || item.label}>
     <Sidebar.MenuIcon><Icon d={icons[item.icon]}/></Sidebar.MenuIcon>
     <Sidebar.MenuLabel>{item.label}</Sidebar.MenuLabel>
     {item.live || item.count || item.kbd ? <Sidebar.MenuChip>
      {item.live ? <span className="spinner" aria-hidden="true"/> : null}
      {item.count ? <span className="side-pro-count">{item.count}</span> : null}
      {item.kbd ? <Kbd className="side-pro-kbd"><Kbd.Content>{item.kbd}</Kbd.Content></Kbd> : null}
     </Sidebar.MenuChip> : null}
    </Sidebar.MenuItem>)}
   </Sidebar.Menu>
  </Sidebar.Header>
  <Sidebar.Content className="side-pro-content">
   {compact ? null : <Sidebar.Group>
    <Sidebar.GroupLabel>{labels.sessions}</Sidebar.GroupLabel>
    {props.groups.length ? <Sidebar.Menu aria-label={labels.sessions} className="side-pro-tree" expandedKeys={expanded} onExpandedChange={expandedChange} onAction={treeAction} showGuideLines={false}>
     {props.groups.map((g) => <Sidebar.MenuItem key={g.id} id={`${prefix}ws:${g.id}`} textValue={g.name} tooltipProps={{content: g.title, delay: 700, placement: 'right'}}>
      <Sidebar.MenuIcon><Mark mark={g.mark}/></Sidebar.MenuIcon>
      <Sidebar.MenuLabel>{g.name}<Sidebar.MenuTrigger><Sidebar.MenuIndicator/></Sidebar.MenuTrigger></Sidebar.MenuLabel>
      {g.closed && g.live ? <Sidebar.MenuChip><span className="spinner" aria-hidden="true"/></Sidebar.MenuChip> : null}
      <Sidebar.MenuActions>
       <Sidebar.MenuAction aria-label={g.addLabel} onPress={() => props.onDraft(g.id)}><Icon d={icons.plus} size="xs"/></Sidebar.MenuAction>
      </Sidebar.MenuActions>
      <Sidebar.Submenu>
       {g.drafting ? <Sidebar.MenuItem id={`${prefix}draft:${g.id}`} textValue={labels.draft} isCurrent className="side-pro-draft">
        <Sidebar.MenuIcon><Icon d={icons.compose} size="xs"/></Sidebar.MenuIcon>
        <Sidebar.MenuLabel>{labels.draft}</Sidebar.MenuLabel>
       </Sidebar.MenuItem> : null}
       {g.sessions.map((s) => <Sidebar.MenuItem key={s.id} id={`${prefix}s:${g.id}/${s.id}`} textValue={s.title} isCurrent={s.current} className="side-pro-session">
        <Sidebar.MenuIcon>{s.live ? <span className="spinner" title={labels.live}/> : s.mark ? <Mark mark={s.mark} size="xs"/> : <Icon d={icons.chat} size="xs"/>}</Sidebar.MenuIcon>
        <Sidebar.MenuLabel>{s.title}</Sidebar.MenuLabel>
        {s.when ? <Sidebar.MenuChip><span className="side-pro-when">{s.when}</span></Sidebar.MenuChip> : null}
        <Sidebar.MenuActions>
         <Dropdown>
          <Sidebar.MenuAction aria-label={labels.sessionMenu}><Icon d={icons.more} size="xs"/></Sidebar.MenuAction>
          <Dropdown.Popover placement="bottom start" className="side-pro-popover">
           <Dropdown.Menu aria-label={labels.sessionMenu} onAction={(key) => props.onSessionAction(String(key), g.id, s.id)}>
            <Dropdown.Item id="rename" textValue={labels.rename}><Icon d={icons.edit} className="side-pro-item-icon"/><Label>{labels.rename}</Label></Dropdown.Item>
            <Dropdown.Item id="duplicate" textValue={labels.duplicate}><Icon d={icons.copy} className="side-pro-item-icon"/><div className="side-pro-item-text"><Label>{labels.duplicate}</Label><Description>{labels.duplicateDesc}</Description></div></Dropdown.Item>
            <Separator/>
            <Dropdown.Item id="delete" textValue={labels.remove} variant="danger"><Icon d={icons.trash} className="side-pro-item-icon danger"/><Label>{labels.remove}</Label></Dropdown.Item>
           </Dropdown.Menu>
          </Dropdown.Popover>
         </Dropdown>
        </Sidebar.MenuActions>
       </Sidebar.MenuItem>)}
       {g.moreLabel ? <Sidebar.MenuItem id={`${prefix}more:${g.id}`} textValue={g.moreLabel} className="side-pro-more"><Sidebar.MenuLabel>{g.moreLabel}</Sidebar.MenuLabel></Sidebar.MenuItem> : null}
       {!g.total && !g.drafting ? <Sidebar.MenuItem id={`${prefix}empty:${g.id}`} textValue={labels.noSessions} className="side-pro-more"><Sidebar.MenuLabel>{labels.noSessions}</Sidebar.MenuLabel></Sidebar.MenuItem> : null}
      </Sidebar.Submenu>
     </Sidebar.MenuItem>)}
    </Sidebar.Menu> : <p className="side-pro-empty">{labels.noWorkspaces}</p>}
   </Sidebar.Group>}
  </Sidebar.Content>
  <Sidebar.Footer className="side-pro-foot">
   <Dropdown>
    <Dropdown.Trigger className="side-pro-profile" aria-label={labels.userMenu}>
     <Avatar size="sm" className="side-pro-avatar">
      {props.user.avatarUrl ? <Avatar.Image alt="" src={props.user.avatarUrl}/> : null}
      <Avatar.Fallback>{props.user.initials}</Avatar.Fallback>
     </Avatar>
     <span className="side-pro-who" data-sidebar="label">
      <span className="side-pro-who-name">{props.user.name}</span>
      <span className="side-pro-who-detail">{props.user.detail}</span>
     </span>
     <span className="side-pro-profile-more" data-sidebar="label"><Icon d={icons.more}/></span>
    </Dropdown.Trigger>
    <Dropdown.Popover placement="top start" className="side-pro-popover side-pro-user-menu">
     <Dropdown.Menu aria-label={labels.userMenu} onAction={(key) => {const id = String(key); if (!id.includes(':')) props.onUserAction(id);}}>
      <Dropdown.Item id="settings" textValue={labels.settings}><Icon d={icons.box} className="side-pro-item-icon"/><Label>{labels.settings}</Label></Dropdown.Item>
      <Dropdown.Item id="account" textValue={labels.account}><Icon d={icons.link} className="side-pro-item-icon"/><div className="side-pro-item-text"><Label>{labels.account}</Label><Description>{labels.accountDesc}</Description></div></Dropdown.Item>
      <Separator/>
      <Dropdown.Section selectionMode="single" disallowEmptySelection selectedKeys={new Set([`theme:${props.theme}`])} onSelectionChange={(keys) => {const id = only(keys); if (id) props.onUserAction('theme', id.slice('theme:'.length));}}>
       <Header>{labels.appearance}</Header>
       {props.themes.map((item) => <Dropdown.Item key={item.id} id={`theme:${item.id}`} textValue={item.label}><Dropdown.ItemIndicator/><Label>{item.label}</Label></Dropdown.Item>)}
      </Dropdown.Section>
      <Separator/>
      <Dropdown.Section selectionMode="single" disallowEmptySelection selectedKeys={new Set([`lang:${props.language}`])} onSelectionChange={(keys) => {const id = only(keys); if (id) props.onUserAction('language', id.slice('lang:'.length));}}>
       <Header>{labels.language}</Header>
       {props.languages.map((item) => <Dropdown.Item key={item.id} id={`lang:${item.id}`} textValue={item.label}><Dropdown.ItemIndicator/><Label>{item.label}</Label></Dropdown.Item>)}
      </Dropdown.Section>
      <Separator/>
      <Dropdown.Item id="logout" textValue={labels.logout}><Icon d={icons.logout} className="side-pro-item-icon"/><Label>{labels.logout}</Label></Dropdown.Item>
     </Dropdown.Menu>
    </Dropdown.Popover>
   </Dropdown>
  </Sidebar.Footer>
 </>;
}

type SidebarApi = ReturnType<typeof useSidebar>;
function Desktop(props: SidebarProps & {api: React.MutableRefObject<SidebarApi | null>}) {
 const api = useSidebar();
 useEffect(() => {props.api.current = api;});
 return <>
  <Sidebar className="side-pro" data-pro-component="Sidebar">
   <SidebarBody {...props} prefix="d-" compact={!api.isOpen}/>
   <Sidebar.Rail/>
  </Sidebar>
  <Sidebar.Mobile><SidebarBody {...props} prefix="m-" compact={false}/></Sidebar.Mobile>
 </>;
}
export function AppSidebar(props: SidebarProps & {api: React.MutableRefObject<SidebarApi | null>}) {
 return <Sidebar.Provider className="side-pro-provider" collapsible="icon" open={!props.collapsed} onOpenChange={(open: boolean) => props.onCollapsedChange(!open)}>
  <Desktop {...props}/>
 </Sidebar.Provider>;
}
