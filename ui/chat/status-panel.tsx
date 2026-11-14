import React, {useLayoutEffect, useRef} from 'react';
import {Accordion} from '@heroui/react';
import {Icon, type IconName} from './icon';

// The session's status sections (environment, context, output, changes, background) as one HeroUI Accordion.
// The controller still draws each section's body: the component hands it one host element per section
// through onPanel, and the controller redraws the bodies of the expanded sections on every event.
export interface StatusSummary {text?: string; tone?: 'ok' | 'err' | 'warn'; busy?: boolean; added?: number; deleted?: number}
export interface StatusSection {id: string; icon: IconName; title: string; summary: StatusSummary}
export interface StatusPanelProps {
 label: string; sections: StatusSection[]; expanded: string[];
 onExpandedChange(ids: string[]): void; onPanel(element: HTMLElement, id: string): void;
}

function SectionHost({id, onPanel}: {id: string; onPanel(element: HTMLElement, id: string): void}) {
 const ref = useRef<HTMLDivElement>(null);
 const latest = useRef(onPanel);
 latest.current = onPanel;
 useLayoutEffect(() => {if (ref.current) latest.current(ref.current, id);}, [id]);
 return <div ref={ref} className="sp-body" data-section={id}/>;
}

function Summary({summary}: {summary: StatusSummary}) {
 const {text, tone, busy, added, deleted} = summary;
 return <span className="sum">
  {busy ? <span className="spinner" aria-hidden="true"/> : null}
  {tone ? <span className={`dot ${tone}`} aria-hidden="true"/> : null}
  {added != null ? <span className="stat-add">+{added}</span> : null}
  {deleted != null ? <span className="stat-del">−{deleted}</span> : null}
  {text ? <span className="sum-text">{text}</span> : null}
 </span>;
}

export function StatusPanel(p: StatusPanelProps) {
 return <Accordion className="status-accordion" aria-label={p.label} allowsMultipleExpanded hideSeparator
  expandedKeys={new Set(p.expanded)} onExpandedChange={(keys) => p.onExpandedChange([...keys].map(String))}>
  {p.sections.map((section) => {
   const open = p.expanded.includes(section.id);
   return <Accordion.Item key={section.id} id={section.id} className={`sp${open ? ' open' : ''}`} data-section={section.id}>
    <Accordion.Heading className="sp-heading">
     <Accordion.Trigger className="sp-head">
      <span className="sp-icon"><Icon name={section.icon}/></span>
      <span className="t">{section.title}</span>
      <Summary summary={section.summary}/>
      <Accordion.Indicator className="sp-indicator"><Icon name="chevDown" size="xs"/></Accordion.Indicator>
     </Accordion.Trigger>
    </Accordion.Heading>
    <Accordion.Panel className="sp-panel">
     <Accordion.Body className="sp-panel-body"><SectionHost id={section.id} onPanel={p.onPanel}/></Accordion.Body>
    </Accordion.Panel>
   </Accordion.Item>;
  })}
 </Accordion>;
}
