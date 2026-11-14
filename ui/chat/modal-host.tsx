import React, {useLayoutEffect, useRef} from 'react';
import {flushSync} from 'react-dom';
import {createRoot} from 'react-dom/client';
import {Modal} from '@heroui/react';

// The controller's own dialogs (account, new workspace, repositories, environment editor, entry points and the
// rest) are HeroUI Modals. The controller still builds each dialog as a <dialog> element and keeps its handlers;
// the Modal hosts that element, opened non-modally (the open attribute), so it stays in the page's layer. The
// Modal provides the backdrop, focus containment and restoration, scroll lock, Escape and stacking with the
// HeroUI AlertDialog. Escape reports onDismiss; the controller decides, as a native dialog's cancel event would.
export interface HostedDialog {id: number; content: HTMLElement; label: string}
export interface ModalHostProps {dialogs: HostedDialog[]; onDismiss(id: number): void}

// A text field host (textField() in app.js) counts too: its input renders a microtask later, and the host's focus()
// waits for it.
const FOCUSABLE = 'input:not([type=hidden]):not([disabled]),select:not([disabled]),textarea:not([disabled]),button:not([disabled]),a[href],[tabindex]:not([tabindex="-1"]),.hero-text:not([data-disabled])';

function Hosted({entry, onDismiss}: {entry: HostedDialog; onDismiss(id: number): void}) {
 const ref = useRef<HTMLDivElement>(null);
 useLayoutEffect(() => {
  const host = ref.current;
  if (!host || entry.content.parentNode === host) return;
  host.appendChild(entry.content);
  // As showModal() would: the element marked autofocus, else the first focusable one.
  const target = entry.content.querySelector<HTMLElement>('[autofocus]') || entry.content.querySelector<HTMLElement>(FOCUSABLE);
  target?.focus();
 }, [entry.content]);
 return <Modal.Backdrop className="app-modal-backdrop" isOpen isDismissable={false} onOpenChange={(open: boolean) => {if (!open) onDismiss(entry.id);}}>
  <Modal.Container className="app-modal-container" placement="center">
   <Modal.Dialog className="app-modal" aria-label={entry.label} data-pro-component="Modal">
    <div ref={ref} className="app-modal-host"/>
   </Modal.Dialog>
  </Modal.Container>
 </Modal.Backdrop>;
}

export function ModalHost({dialogs, onDismiss}: ModalHostProps) {
 return <>{dialogs.map((entry) => <Hosted key={entry.id} entry={entry} onDismiss={onDismiss}/>)}</>;
}

// Renders synchronously, so code that calls showDialog() and then focuses or measures an element finds it in place.
export function mountModalHost(container: HTMLElement, initial: ModalHostProps) {
 const root = createRoot(container);
 let disposed = false;
 const update = (props: ModalHostProps) => {if (!disposed) flushSync(() => root.render(<ModalHost {...props}/>));};
 update(initial);
 return {update, unmount: () => {if (!disposed) {disposed = true; root.unmount();}}};
}
