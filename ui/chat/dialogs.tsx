import React, {useEffect, useRef, useState} from 'react';
import {AlertDialog, Button, Input, Label, Modal, TextField} from '@heroui/react';

// Confirmation and single-value input dialogs. The controller opens one by setting a request and gets
// the answer through the callback; a request stays rendered while its closing animation runs.
export interface ConfirmRequest {id: number; title: string; message: string; confirmLabel: string; cancelLabel: string; danger: boolean}
export interface PromptRequest {id: number; title: string; label: string; value: string; confirmLabel: string; cancelLabel: string}
export interface DialogsProps {
 confirm: ConfirmRequest | null; prompt: PromptRequest | null;
 onConfirm(id: number, ok: boolean): void; onPrompt(id: number, value: string | null): void;
}

function useLast<T>(value: T | null): T | null {
 const last = useRef<T | null>(value);
 if (value) last.current = value;
 return value || last.current;
}

function ConfirmDialog({request, onResult}: {request: ConfirmRequest | null; onResult(id: number, ok: boolean): void}) {
 const shown = useLast(request);
 if (!shown) return null;
 return <AlertDialog.Backdrop isOpen={!!request} isKeyboardDismissDisabled={false} onOpenChange={(open: boolean) => {if (!open && request) onResult(request.id, false);}}>
  <AlertDialog.Container size="sm">
   <AlertDialog.Dialog className="app-dialog" data-pro-component="AlertDialog">
    <AlertDialog.Header>
     <AlertDialog.Icon status={shown.danger ? 'danger' : 'accent'}/>
     <AlertDialog.Heading>{shown.title}</AlertDialog.Heading>
    </AlertDialog.Header>
    <AlertDialog.Body><p className="app-dialog-message">{shown.message}</p></AlertDialog.Body>
    <AlertDialog.Footer>
     <Button autoFocus variant="tertiary" onPress={() => onResult(shown.id, false)}>{shown.cancelLabel}</Button>
     <Button variant={shown.danger ? 'danger' : 'primary'} onPress={() => onResult(shown.id, true)}>{shown.confirmLabel}</Button>
    </AlertDialog.Footer>
   </AlertDialog.Dialog>
  </AlertDialog.Container>
 </AlertDialog.Backdrop>;
}

function PromptDialog({request, onResult}: {request: PromptRequest | null; onResult(id: number, value: string | null): void}) {
 const shown = useLast(request);
 const [value, setValue] = useState('');
 const id = request?.id;
 useEffect(() => {if (request) setValue(request.value);}, [id]);
 if (!shown) return null;
 const submit = () => {const text = value.trim(); if (text) onResult(shown.id, text);};
 return <Modal.Backdrop isOpen={!!request} onOpenChange={(open: boolean) => {if (!open && request) onResult(request.id, null);}}>
  <Modal.Container size="sm">
   <Modal.Dialog className="app-dialog" data-pro-component="Modal">
    <Modal.CloseTrigger/>
    <Modal.Header><Modal.Heading>{shown.title}</Modal.Heading></Modal.Header>
    <Modal.Body>
     <form className="app-dialog-form" onSubmit={(event) => {event.preventDefault(); submit();}}>
      <TextField autoFocus className="app-dialog-field" name="value" value={value} variant="secondary" onChange={setValue}>
       <Label>{shown.label}</Label>
       <Input/>
      </TextField>
     </form>
    </Modal.Body>
    <Modal.Footer>
     <Button variant="tertiary" onPress={() => onResult(shown.id, null)}>{shown.cancelLabel}</Button>
     <Button isDisabled={!value.trim()} onPress={submit}>{shown.confirmLabel}</Button>
    </Modal.Footer>
   </Modal.Dialog>
  </Modal.Container>
 </Modal.Backdrop>;
}

export function Dialogs({confirm, prompt, onConfirm, onPrompt}: DialogsProps) {
 return <>
  <ConfirmDialog request={confirm} onResult={onConfirm}/>
  <PromptDialog request={prompt} onResult={onPrompt}/>
 </>;
}
