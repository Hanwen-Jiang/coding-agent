import React from 'react';
import {Button, Card, Chip, Dropdown, Label} from '@heroui/react';
import {EmptyState} from '@heroui-pro/react/empty-state';
import {ItemCard} from '@heroui-pro/react/item-card';
import {Icon, Mark, type SideMark} from './sidebar';

// The three list pages: workspaces, running tasks and environment configurations. The controller supplies
// serializable rows and performs every action; these components own layout, empty states and controls.
type Icons = Record<string, string>;
const TONES: Record<string, 'accent' | 'success' | 'danger' | 'warning' | 'default'> = {run: 'accent', ok: 'success', err: 'danger', warn: 'warning'};
const firstKey = (keys: unknown): string | undefined => {
 if (!keys || keys === 'all') return undefined;
 for (const key of keys as Set<React.Key>) return String(key);
 return undefined;
};

function PageHead({title, lead, children}: {title: string; lead?: string; children?: React.ReactNode}) {
 return <div className="page-head"><div className="grow"><h1>{title}</h1>{lead ? <p>{lead}</p> : null}</div>{children}</div>;
}

export interface WorkspaceCard {
 id: string; name: string; source: string; mark: SideMark; sessions: string; running?: string; recentAgo?: string; multi: boolean;
 recent: Array<{id: string; title: string; when: string; live: boolean}>;
}
export interface WorkspacesProps {
 icons: Icons; title: string; lead: string; workspaces: WorkspaceCard[];
 labels: {create: string; newSession: string; manage: string; remove: string; welcome: string};
 starters: Array<{id: string; icon: string; title: string; desc: string}>;
 onCreate(): void; onNewSession(id: string): void; onOpenSession(project: string, session: string): void;
 onManage(id: string): void; onDelete(id: string): void; onStarter(id: string): void;
}
export function WorkspacesPage(p: WorkspacesProps) {
 return <div className="page pages-pro" data-pro-component="Card">
  <PageHead title={p.title} lead={p.lead}><Button onPress={p.onCreate}><Icon d={p.icons.plus}/>{p.labels.create}</Button></PageHead>
  {p.workspaces.length ? <div className="pages-grid">
   {p.workspaces.map((w) => <Card key={w.id} className="pages-card" data-workspace-id={w.id}>
    <Card.Header className="pages-card-head">
     <span className="pages-card-mark"><Mark mark={w.mark}/></span>
     <div className="pages-card-titles">
      <Card.Title className="pages-card-title">{w.name}</Card.Title>
      <Card.Description className="pages-card-desc">{w.source}</Card.Description>
     </div>
    </Card.Header>
    <Card.Content className="pages-card-body">
     <div className="pages-card-stats">
      <span>{w.sessions}</span>
      {w.running ? <Chip className="run" color="accent" size="sm" variant="soft"><span className="spinner" aria-hidden="true"/>{w.running}</Chip> : null}
      {w.recentAgo ? <span className="pages-muted">{w.recentAgo}</span> : null}
     </div>
     {w.recent.length ? <ul className="pages-recent">
      {w.recent.map((s) => <li key={s.id}><button type="button" className="pages-recent-row" onClick={() => p.onOpenSession(w.id, s.id)}>
       {s.live ? <span className="spinner" aria-hidden="true"/> : <Icon d={p.icons.chat} size="xs"/>}
       <span className="pages-recent-title">{s.title}</span><span className="pages-recent-when">{s.when}</span>
      </button></li>)}
     </ul> : null}
    </Card.Content>
    <Card.Footer className="pages-card-foot">
     <Button size="sm" variant="secondary" onPress={() => p.onNewSession(w.id)}><Icon d={p.icons.plus}/>{p.labels.newSession}</Button>
     {w.multi ? <Button size="sm" variant="outline" onPress={() => p.onManage(w.id)}>{p.labels.manage}</Button> : null}
     <span className="grow"/>
     <Button isIconOnly aria-label={p.labels.remove} className="pages-quiet-danger" size="sm" variant="ghost" onPress={() => p.onDelete(w.id)}><Icon d={p.icons.trash}/></Button>
    </Card.Footer>
   </Card>)}
  </div> : <EmptyState className="pages-empty" size="lg">
   <EmptyState.Header>
    <EmptyState.Media variant="icon"><Icon d={p.icons.grid} size="lg"/></EmptyState.Media>
    <EmptyState.Title>{p.labels.welcome}</EmptyState.Title>
   </EmptyState.Header>
   <EmptyState.Content className="pages-starters">
    {p.starters.map((s) => <button key={s.id} type="button" className="suggestion" onClick={() => p.onStarter(s.id)}>
     <Icon d={p.icons[s.icon]} size=""/><div><div className="t">{s.title}</div><div className="d">{s.desc}</div></div>
    </button>)}
   </EmptyState.Content>
  </EmptyState>}
 </div>;
}

export interface ActivityRow {key: string; title: string; meta: string; running: boolean; status: string; tone: string}
export interface ActivityProps {
 icons: Icons; title: string; rows: ActivityRow[];
 filter: {label: string; value: string; options: Array<{id: string; label: string}>};
 labels: {stop: string; empty: string; newSession: string};
 onFilter(value: string): void; onOpen(key: string): void; onStop(key: string): void; onNewSession(): void;
}
export function ActivityPage(p: ActivityProps) {
 const current = p.filter.options.find((option) => option.id === p.filter.value) || p.filter.options[0];
 return <div className="page pages-pro" data-pro-component="ItemCard">
  <PageHead title={p.title}>
   <Dropdown>
    <Button aria-label={p.filter.label} className="pages-filter" size="sm" variant="outline">{current?.label}<Icon d={p.icons.chevDown} size="xs"/></Button>
    <Dropdown.Popover className="side-pro-popover" placement="bottom end">
     <Dropdown.Menu aria-label={p.filter.label} disallowEmptySelection selectedKeys={new Set([p.filter.value])} selectionMode="single"
      onSelectionChange={(keys) => {const id = firstKey(keys); if (id) p.onFilter(id);}}>
      {p.filter.options.map((option) => <Dropdown.Item key={option.id} id={option.id} textValue={option.label}><Dropdown.ItemIndicator/><Label>{option.label}</Label></Dropdown.Item>)}
     </Dropdown.Menu>
    </Dropdown.Popover>
   </Dropdown>
  </PageHead>
  {p.rows.length ? <div className="pages-list">
   {p.rows.map((row) => <ItemCard key={row.key} className="pages-row" role="button" tabIndex={0} variant="outline"
    onClick={() => p.onOpen(row.key)} onKeyDown={(event) => {if (event.key === 'Enter' && event.target === event.currentTarget) p.onOpen(row.key);}}>
    <ItemCard.Icon>{row.running ? <span className="spinner" aria-hidden="true"/> : <Icon d={p.icons.clock}/>}</ItemCard.Icon>
    <ItemCard.Content><ItemCard.Title>{row.title}</ItemCard.Title><ItemCard.Description>{row.meta}</ItemCard.Description></ItemCard.Content>
    <ItemCard.Action onClick={(event) => event.stopPropagation()}>
     <Chip className={row.tone || undefined} color={TONES[row.tone] || 'default'} size="sm" variant="soft">{row.status}</Chip>
     <Button size="sm" variant="outline" onPress={() => p.onStop(row.key)}><Icon d={p.icons.stop} size="xs"/>{p.labels.stop}</Button>
    </ItemCard.Action>
   </ItemCard>)}
  </div> : <EmptyState className="pages-empty">
   <EmptyState.Header>
    <EmptyState.Media variant="icon"><Icon d={p.icons.check} size="lg"/></EmptyState.Media>
    <EmptyState.Title>{p.labels.empty}</EmptyState.Title>
   </EmptyState.Header>
   <EmptyState.Content><Button size="sm" variant="outline" onPress={p.onNewSession}><Icon d={p.icons.compose}/>{p.labels.newSession}</Button></EmptyState.Content>
  </EmptyState>}
 </div>;
}

export interface EnvironmentCard {
 id: string; name: string; description: string; usage: string;
 chips: Array<{icon?: string; label: string; tone?: string; title?: string; mono?: boolean}>;
}
export interface EnvironmentsProps {
 icons: Icons; title: string; lead: string; loading: boolean; environments: EnvironmentCard[];
 labels: {create: string; edit: string; remove: string; empty: string; loading: string};
 onCreate(): void; onEdit(id: string): void; onDelete(id: string): void;
}
export function EnvironmentsPage(p: EnvironmentsProps) {
 return <div className="page pages-pro" data-pro-component="Card">
  <PageHead title={p.title} lead={p.lead}><Button data-environment-create="" onPress={p.onCreate}><Icon d={p.icons.plus}/>{p.labels.create}</Button></PageHead>
  {p.loading ? <div className="row muted"><span className="spinner"/>{p.labels.loading}</div>
   : p.environments.length ? <div className="pages-grid">
    {p.environments.map((env) => <Card key={env.id} className="pages-card" data-environment-id={env.id}>
     <Card.Header className="pages-card-head">
      <span className="pages-card-mark"><Icon d={p.icons.server}/></span>
      <div className="pages-card-titles">
       <Card.Title className="pages-card-title">{env.name}</Card.Title>
       {env.description ? <Card.Description className="pages-card-desc pages-card-desc-wrap">{env.description}</Card.Description> : null}
      </div>
     </Card.Header>
     <Card.Content className="pages-card-body">
      <div className="pages-chips">
       {env.chips.map((chip, i) => <Chip key={i} className={[chip.tone, chip.mono ? 'mono' : ''].filter(Boolean).join(' ') || undefined} color={TONES[chip.tone || ''] || 'default'} size="sm" title={chip.title} variant="soft">
        {chip.icon ? <Icon d={p.icons[chip.icon]} size="xs"/> : null}{chip.label}
       </Chip>)}
      </div>
      <div className="pages-card-stats"><span className="pages-muted">{env.usage}</span></div>
     </Card.Content>
     <Card.Footer className="pages-card-foot">
      <Button data-environment-edit="" size="sm" variant="secondary" onPress={() => p.onEdit(env.id)}><Icon d={p.icons.edit}/>{p.labels.edit}</Button>
      <span className="grow"/>
      <Button isIconOnly aria-label={p.labels.remove} className="pages-quiet-danger" data-environment-delete="" size="sm" variant="ghost" onPress={() => p.onDelete(env.id)}><Icon d={p.icons.trash}/></Button>
     </Card.Footer>
    </Card>)}
   </div> : <EmptyState className="pages-empty">
    <EmptyState.Header>
     <EmptyState.Media variant="icon"><Icon d={p.icons.server} size="lg"/></EmptyState.Media>
     <EmptyState.Title>{p.labels.empty}</EmptyState.Title>
    </EmptyState.Header>
    <EmptyState.Content><Button size="sm" variant="outline" onPress={p.onCreate}><Icon d={p.icons.plus}/>{p.labels.create}</Button></EmptyState.Content>
   </EmptyState>}
 </div>;
}
