import React, {useLayoutEffect, useRef} from 'react';
import {Tabs} from '@heroui/react';

// Settings: HeroUI Tabs own the section switcher, keyboard navigation and the tab panel. The panel's content
// (personal configuration, Skills, MCP and entry points) is still drawn by the controller into a host element
// that React hands over through onPanel; React never renders children into that host, so it leaves them alone.
export interface SettingsProps {
 label: string;
 selected: string;
 sections: Array<{id: string; label: string}>;
 onSelect(id: string): void;
 onPanel(element: HTMLElement, id: string): void;
}

function PanelHost({id, onPanel}: {id: string; onPanel: SettingsProps['onPanel']}) {
 const ref = useRef<HTMLDivElement>(null);
 const latest = useRef(onPanel);
 latest.current = onPanel;
 useLayoutEffect(() => {
  if (ref.current) latest.current(ref.current, id);
 }, [id]);
 return <div ref={ref} className="settings-content" data-settings-section={id}/>;
}

export function SettingsTabs(p: SettingsProps) {
 return <Tabs className="settings-tabs-pro" selectedKey={p.selected} variant="secondary"
  onSelectionChange={(key) => {if (String(key) !== p.selected) p.onSelect(String(key));}}>
  <Tabs.ListContainer className="settings-tab-bar">
   <Tabs.List aria-label={p.label}>
    {p.sections.map((section) => <Tabs.Tab key={section.id} id={section.id} className="settings-tab">{section.label}<Tabs.Indicator/></Tabs.Tab>)}
   </Tabs.List>
  </Tabs.ListContainer>
  <Tabs.Panel key={p.selected} id={p.selected} className="settings-panel">
   <PanelHost id={p.selected} onPanel={p.onPanel}/>
  </Tabs.Panel>
 </Tabs>;
}
