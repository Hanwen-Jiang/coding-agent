import React, {useLayoutEffect, useRef, useState, useSyncExternalStore} from 'react';
import {PromptInput} from '@heroui-pro/react/prompt-input';
import {Button} from '@heroui/react';
import {ChatAttachment} from '@heroui-pro/react/chat-attachment';

type Upload = {id:string;name:string;size_bytes:number;mime_type:string;preview_url?:string;status:string;error?:string;version_label?:string};
interface ComposerSnapshot {
 attachments?:Upload[];
 collectionLabel?:string;
 canAttach?:boolean;
 uploadLabels?:{add:string;remove:string;retry:string;uploading:string;ready:string;pending:string;drop:string;unavailable:string};
 mode: string;
 placeholder: string;
 shortcuts: string;
 disabled: boolean;
 label: string;
 sendLabel: string;
 menuLabel: string;
}
export interface ComposerProps {
 model: {
  id: number;
  subscribe: (listener: () => void) => () => void;
  getSnapshot: () => ComposerSnapshot;
 };
 input: HTMLTextAreaElement;
 slots: {status: HTMLElement; ask: HTMLElement; options: HTMLElement; help: HTMLElement; tools: HTMLElement};
 bind: (input: HTMLTextAreaElement, body: HTMLDivElement, right: HTMLDivElement, send: HTMLButtonElement | null) => void;
 onFiles:(files:File[])=>void;
 onRemoveUpload:(id:string)=>void;
 onRemoveCollection?:()=>void;
 onRetryUpload:(id:string)=>void;
 onInput: () => void;
 onKeyDown: (event: KeyboardEvent) => void;
 onSend: () => void;
 onMenu: (anchor: HTMLElement) => void;
}

// These are the remaining application-owned approval/status controls. The
// editor and its send actions below are owned by React, including their refs.
function ControlSlot({node,className=""}: {node: HTMLElement;className?:string}) {
 const ref=useRef<HTMLDivElement>(null);
 useLayoutEffect(()=>{ref.current?.replaceChildren(node);},[node]);
 return <div ref={ref} className={"composer-control-slot "+className}/>;
}

export function Composer(props: ComposerProps) {
 return <ComposerView key={props.model.id} {...props}/>;
}

function ComposerView(props: ComposerProps) {
 const state=useSyncExternalStore(props.model.subscribe,props.model.getSnapshot);
 const fileInput=useRef<HTMLInputElement>(null);
 const dragDepth=useRef(0);
 const [dragging,setDragging]=useState(false);
 const input=useRef<HTMLTextAreaElement>(null);
 const body=useRef<HTMLDivElement>(null);
 const right=useRef<HTMLDivElement>(null);
 const send=useRef<HTMLButtonElement>(null);
 const menu=useRef<HTMLButtonElement>(null);
 const initial=useRef(props.input.value);
 const wasFocused=useRef(document.activeElement===props.input);
 const initialSelection=useRef([props.input.selectionStart,props.input.selectionEnd]);
 useLayoutEffect(()=>{
  const previous=props.input;
  // Preserve edits entered while the React bundle was loading.
  input.current!.value=previous.value;
  props.bind(input.current!,body.current!,right.current!,send.current);
  if(wasFocused.current){input.current!.focus();input.current!.setSelectionRange(initialSelection.current[0],initialSelection.current[1]);}
 },[]);
 return <PromptInput layout="stacked" lockInputOnRun={false} allowSubmitWhileRunning
  status={state.mode==='running'?'streaming':'ready'} maxHeight={260}
  className="pro-prompt" data-pro-component="PromptInput" data-react-composer="true" data-dragging={dragging}
  onDragEnter={event=>{if(event.dataTransfer.types.includes('Files')){event.preventDefault();dragDepth.current++;if(state.canAttach)setDragging(true);}}}
  onDragOver={event=>{if(event.dataTransfer.types.includes('Files')){event.preventDefault();event.dataTransfer.dropEffect=state.canAttach?'copy':'none';}}}
  onDragLeave={event=>{if(event.dataTransfer.types.includes('Files')){dragDepth.current=Math.max(0,dragDepth.current-1);if(!dragDepth.current)setDragging(false);}}}
  onDrop={event=>{if(event.dataTransfer.types.includes('Files')){event.preventDefault();dragDepth.current=0;setDragging(false);props.onFiles(Array.from(event.dataTransfer.files));}}}
  onPaste={event=>{if(event.clipboardData.files.length){event.preventDefault();props.onFiles(Array.from(event.clipboardData.files));}}}>
  <input ref={fileInput} type="file" className="composer-file-input" multiple hidden aria-label={state.uploadLabels?.add} onChange={event=>{props.onFiles(Array.from(event.target.files||[]));event.target.value='';}}/>
  <PromptInput.Shell>
   <div ref={body} className={'composer'+(state.mode==='waiting'?' waiting':'')}>
    <ControlSlot node={props.slots.status}/><ControlSlot node={props.slots.ask}/>
    {state.collectionLabel&&<div className="composer-attachments" data-knowledge-collection><span className="composer-attachment"><span>{state.collectionLabel}</span><Button size="sm" variant="ghost" isIconOnly aria-label={state.uploadLabels?.remove} onPress={props.onRemoveCollection}>×</Button></span></div>}
    {!!state.attachments?.length&&<div className="composer-attachments" aria-live="polite">{state.attachments.map(file=><div key={file.id} className="composer-attachment" data-upload-id={file.id} data-upload-state={file.status}>
     <ChatAttachment className="composer-attachment-file" mediaType="document" name={file.name} size={file.size_bytes}>
      {file.preview_url?<ChatAttachment.Preview><img className="composer-attachment-preview" src={file.preview_url} alt=""/></ChatAttachment.Preview>:<ChatAttachment.Preview/>}
      <ChatAttachment.Info><ChatAttachment.Name/>{file.version_label&&<small className="upload-version">{file.version_label}</small>}<span className="upload-state">{file.status==='uploading'?state.uploadLabels?.uploading:file.status==='ready'?state.uploadLabels?.ready:file.status==='error'?file.error:state.uploadLabels?.pending}</span></ChatAttachment.Info>
     </ChatAttachment>
     {file.status==='error'&&<Button size="sm" variant="ghost" className="upload-retry" onPress={()=>props.onRetryUpload(file.id)}>{state.uploadLabels?.retry}</Button>}
     <Button size="sm" variant="ghost" isIconOnly className="upload-remove" isDisabled={file.status==='uploading'} aria-label={`${state.uploadLabels?.remove}: ${file.name}`} onPress={()=>props.onRemoveUpload(file.id)}>×</Button>
    </div>)}</div>}
    {dragging&&<div className="composer-drop-hint">{state.uploadLabels?.drop}</div>}
    <PromptInput.Content className="composer-editor"><div className="c-row" onKeyDownCapture={event=>{
     // The installed Pro version submits Enter even during composition. Keep
     // the application's IME and Ctrl/Meta+Enter semantics ahead of that handler.
     if(event.key==='Enter'&&!event.shiftKey){
      event.stopPropagation();
      if(!event.nativeEvent.isComposing&&event.nativeEvent.keyCode!==229)props.onKeyDown(event.nativeEvent);
     }
    }}>
     <PromptInput.TextArea ref={input} id="composer" className="c-input" rows={1}
      aria-label={state.label} aria-describedby="composer-help" placeholder={state.placeholder}
      title={state.shortcuts} value={undefined} defaultValue={initial.current}
      disableAutosize onInput={props.onInput}/>
    </div></PromptInput.Content>
    <ControlSlot node={props.slots.options}/>
    <PromptInput.Toolbar className="composer-toolbar">
     <PromptInput.ToolbarStart className="composer-toolbar-controls"><PromptInput.Action className="composer-attach" variant="ghost" isIconOnly isDisabled={!state.canAttach} tooltip={state.canAttach?state.uploadLabels?.add:state.uploadLabels?.unavailable} aria-label={state.uploadLabels?.add} onPress={()=>fileInput.current?.click()}>
      <svg viewBox="0 0 20 20" width="18" height="18" fill="none" aria-hidden="true"><path d="M10 4v12M4 10h12" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/></svg>
     </PromptInput.Action><ControlSlot node={props.slots.tools} className="composer-tools-slot"/></PromptInput.ToolbarStart>
     <PromptInput.ToolbarEnd><div ref={right} className="c-right">
      {state.mode!=='waiting'?<div className="send-group">
       {state.mode==='running'?<PromptInput.Action ref={menu} className="btn ghost xs send-menu-toggle"
        variant="ghost" aria-label={state.menuLabel} title={state.menuLabel}
        onPress={()=>{if(menu.current)props.onMenu(menu.current);}}>
        <svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>
       </PromptInput.Action>:null}
       <PromptInput.Send ref={send} className="send" aria-label={state.sendLabel}
        title={state.shortcuts||state.sendLabel} isDisabled={state.disabled} onPress={props.onSend}/>
      </div>:null}
     </div></PromptInput.ToolbarEnd>
    </PromptInput.Toolbar>
    <ControlSlot node={props.slots.help}/>
   </div>
  </PromptInput.Shell>
 </PromptInput>;
}
