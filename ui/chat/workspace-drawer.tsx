import React, {useLayoutEffect, useRef} from 'react';
import {Button, Drawer, Tabs} from '@heroui/react';
import {Icon, type IconName} from './icon';

// The session's changes, files and history: a HeroUI Drawer from the right, with Tabs for the three views.
// As in Settings, the controller draws the selected view: the component hands it one host element per
// view through onPanel. Closing (button, Escape, the backdrop) only reports onClose; the controller decides.
export interface DrawerTab {id: string; icon: IconName; label: string; count?: number}
export interface WorkspaceDrawerProps {
 open: boolean; label: string; title: string; subtitle?: string; closeLabel: string;
 tab: string; tabs: DrawerTab[];
 onTab(id: string): void; onClose(): void; onPanel(element: HTMLElement, id: string): void;
}

function ViewHost({id, onPanel}: {id: string; onPanel(element: HTMLElement, id: string): void}) {
 const ref = useRef<HTMLDivElement>(null);
 const latest = useRef(onPanel);
 latest.current = onPanel;
 useLayoutEffect(() => {if (ref.current) latest.current(ref.current, id);}, [id]);
 return <div ref={ref} className="drawer-view" data-drawer-view={id}/>;
}

export function WorkspaceDrawer(p: WorkspaceDrawerProps) {
 return <Drawer.Backdrop className="ws-drawer-backdrop" isOpen={p.open} onOpenChange={(open: boolean) => {if (!open) p.onClose();}}>
  <Drawer.Content placement="right" className="ws-drawer">
   <Drawer.Dialog className="ws-drawer-dialog" aria-label={p.label} data-pro-component="Drawer">
    <Drawer.Header className="drawer-head">
     <div className="ws-drawer-title">
      <Drawer.Heading className="h">{p.title}</Drawer.Heading>
      {p.subtitle ? <span className="ws-drawer-subtitle">{p.subtitle}</span> : null}
     </div>
     <Button className="ws-drawer-close" data-drawer-close="" isIconOnly size="sm" variant="ghost" aria-label={p.closeLabel} onPress={p.onClose}>
      <Icon name="x"/>
     </Button>
    </Drawer.Header>
    <Tabs className="ws-drawer-tabs" selectedKey={p.tab} variant="secondary"
     onSelectionChange={(key) => {if (String(key) !== p.tab) p.onTab(String(key));}}>
     <Tabs.ListContainer className="ws-drawer-tab-bar">
      <Tabs.List aria-label={p.label}>
       {p.tabs.map((tab) => <Tabs.Tab key={tab.id} id={tab.id} className="dtab">
        <Icon name={tab.icon}/>{tab.label}{tab.count ? <span className="n">{tab.count}</span> : null}<Tabs.Indicator/>
       </Tabs.Tab>)}
      </Tabs.List>
     </Tabs.ListContainer>
     <Tabs.Panel key={p.tab} id={p.tab} className="drawer-body">
      <ViewHost id={p.tab} onPanel={p.onPanel}/>
     </Tabs.Panel>
    </Tabs>
   </Drawer.Dialog>
  </Drawer.Content>
 </Drawer.Backdrop>;
}
