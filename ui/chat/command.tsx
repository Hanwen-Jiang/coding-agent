import React from 'react';
import {Command} from '@heroui-pro/react/command';
import {Kbd} from '@heroui/react';
import {Icon, Mark, type SideMark} from './sidebar';

// Search and quick actions (Ctrl/⌘ K). The controller supplies the entries and runs the picked one;
// filtering, keyboard navigation and focus management are the Pro Command's own.
export interface CommandEntry {id: string; title: string; sub?: string; text: string; icon?: string; mark?: SideMark | null}
export interface CommandGroup {id: string; heading: string; items: CommandEntry[]}
export interface CommandProps {
 open: boolean; icons: Record<string, string>; groups: CommandGroup[];
 labels: {title: string; placeholder: string; none: string};
 onOpenChange(open: boolean): void; onPick(group: string, id: string): void;
}

export function CommandPalette({open, icons, groups, labels, onOpenChange, onPick}: CommandProps) {
 return <Command>
  <Command.Backdrop isOpen={open} onOpenChange={onOpenChange} variant="blur">
   <Command.Container size="lg">
    <Command.Dialog aria-label={labels.title} className="pro-command" data-pro-component="Command">
     <Command.InputGroup>
      <Command.InputGroup.Prefix><Icon d={icons.search}/></Command.InputGroup.Prefix>
      <Command.InputGroup.Input placeholder={labels.placeholder} aria-label={labels.title}/>
      <Command.InputGroup.ClearButton/>
      <Command.InputGroup.Suffix><Kbd className="pro-command-kbd"><Kbd.Content>Esc</Kbd.Content></Kbd></Command.InputGroup.Suffix>
     </Command.InputGroup>
     <Command.List
      renderEmptyState={() => <div className="pro-command-empty">{labels.none}</div>}
      onAction={(key) => {const id = String(key), cut = id.indexOf(':'); onPick(id.slice(0, cut), id.slice(cut + 1));}}>
      {groups.filter((group) => group.items.length).map((group) => <Command.Group key={group.id} heading={group.heading}>
       {group.items.map((item) => <Command.Item key={item.id} id={`${group.id}:${item.id}`} textValue={item.text}>
        <span className="pro-command-mark">{item.mark ? <Mark mark={item.mark}/> : <Icon d={icons[item.icon || 'box']}/>}</span>
        <span className="pro-command-title">{item.title}</span>
        {item.sub ? <span className="pro-command-sub">{item.sub}</span> : null}
       </Command.Item>)}
      </Command.Group>)}
     </Command.List>
    </Command.Dialog>
   </Command.Container>
  </Command.Backdrop>
 </Command>;
}
