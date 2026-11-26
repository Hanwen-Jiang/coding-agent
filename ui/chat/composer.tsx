import React, {useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore} from 'react';
import {PromptInput} from '@heroui-pro/react/prompt-input';
import {Button} from '@heroui/react';
import {ChatAttachment} from '@heroui-pro/react/chat-attachment';
import {Icon} from './icon';
import {ImageLightbox} from './lightbox';

type Upload = {id:string;name:string;size_bytes:number;mime_type:string;preview_url?:string;status:string;error?:string;version_label?:string};
export type SlashCommand = {id:string;label:string;description?:string;insert?:string};
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
 hasNewOutput?: boolean;
 newOutputLabel?: string;
 scrollToBottomLabel?: string;
 commands?: SlashCommand[];
 mentionFiles?: string[];
 autocompleteLabels?: {commandsTitle?: string; filesTitle?: string; empty?: string};
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
 onScrollToBottom?: () => void;
 onSlashCommand?: (id: string) => void;
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
 const [showScrollDown,setShowScrollDown]=useState(false);
 const [zoom,setZoom]=useState<{src:string;alt:string}|null>(null);
 const [acState,setAcState]=useState<{trigger:'/'|'@';query:string;start:number;end:number;index:number}|null>(null);
 const input=useRef<HTMLTextAreaElement>(null);
 const body=useRef<HTMLDivElement>(null);
 const right=useRef<HTMLDivElement>(null);
 const send=useRef<HTMLButtonElement>(null);
 const menu=useRef<HTMLButtonElement>(null);
 const initial=useRef(props.input.value);
 const wasFocused=useRef(document.activeElement===props.input);
 const initialSelection=useRef([props.input.selectionStart,props.input.selectionEnd]);

 const acItems=useMemo(()=>{
  if(!acState)return [];
  const q=acState.query.toLowerCase();
  if(acState.trigger==='/'){
   const cmds=state.commands||[];
   return cmds.filter(c=>c.id.toLowerCase().includes(q)||c.label.toLowerCase().includes(q)).slice(0,8).map(c=>({
    id:c.id,label:c.label,hint:c.description||'',kind:'command' as const,insert:c.insert||c.id,
   }));
  }
  const files=state.mentionFiles||[];
  return files.filter(f=>f.toLowerCase().includes(q)).slice(0,8).map(f=>({
   id:f,label:'@'+f,hint:'',kind:'file' as const,insert:'@'+f+' ',
  }));
 },[acState,state.commands,state.mentionFiles]);

 const refreshAutocomplete=(el:HTMLTextAreaElement|null)=>{
  if(!el){setAcState(null);return;}
  const pos=el.selectionStart??el.value.length;
  const before=el.value.slice(0,pos);
  const m=/(?:^|\s)([/@])([^\s]*)$/.exec(before);
  if(!m){setAcState(null);return;}
  const trigger=m[1] as '/'|'@';
  const query=m[2]||'';
  const start=pos-m[1].length-query.length;
  setAcState(prev=>({trigger,query,start,end:pos,index:prev&&prev.trigger===trigger&&prev.start===start?prev.index:0}));
 };

 const applyAutocomplete=(item:{id:string;kind:'command'|'file';insert:string})=>{
  const el=input.current;
  if(!el||!acState){setAcState(null);return;}
  if(item.kind==='command'&&props.onSlashCommand&&!item.insert.endsWith(' ')){
   el.value='';
   el.dispatchEvent(new Event('input',{bubbles:true}));
   setAcState(null);
   props.onSlashCommand(item.id);
   return;
  }
  const v=el.value;
  const next=v.slice(0,acState.start)+item.insert+v.slice(acState.end);
  el.value=next;
  const cursor=acState.start+item.insert.length;
  el.setSelectionRange(cursor,cursor);
  el.dispatchEvent(new Event('input',{bubbles:true}));
  setAcState(null);
  el.focus();
 };
 useEffect(()=>{
  const scroller=document.querySelector<HTMLElement>('.main .scroll');
  if(!scroller)return;
  const check=()=>{setShowScrollDown(scroller.scrollHeight-scroller.clientHeight>80&&scroller.scrollHeight-scroller.scrollTop-scroller.clientHeight>80);};
  let raf=0;
  const scheduleCheck=()=>{if(!raf)raf=requestAnimationFrame(()=>{raf=0;check();});};
  check();
  scroller.addEventListener('scroll',check,{passive:true});
  const observer=new ResizeObserver(scheduleCheck);
  observer.observe(scroller);
  if(scroller.firstElementChild)observer.observe(scroller.firstElementChild);
  return()=>{if(raf)cancelAnimationFrame(raf);scroller.removeEventListener('scroll',check);observer.disconnect();};
 },[]);
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
  {showScrollDown&&<button type="button" className="scroll-to-bottom" data-unread={state.hasNewOutput?'true':undefined}
   aria-label={state.hasNewOutput?(state.newOutputLabel||state.scrollToBottomLabel):(state.scrollToBottomLabel||(document.documentElement.lang.startsWith('zh')?'滚动到底部':'Scroll to bottom'))}
   title={state.scrollToBottomLabel||(document.documentElement.lang.startsWith('zh')?'滚动到底部':'Scroll to bottom')}
   onClick={()=>{const s=document.querySelector<HTMLElement>('.main .scroll');s?.scrollTo({top:s.scrollHeight,behavior:matchMedia('(prefers-reduced-motion: reduce)').matches?'auto':'smooth'});props.onScrollToBottom?.();}}>
   {state.hasNewOutput&&<><span className="scroll-unread-dot" aria-hidden="true"/><span className="scroll-unread-label">{state.newOutputLabel}</span></>}
   <Icon name="chevDown" size="xs"/>
  </button>}
  <PromptInput.Shell>
   <div ref={body} className={'composer'+(state.mode==='waiting'?' waiting':'')}>
    <ControlSlot node={props.slots.status}/><ControlSlot node={props.slots.ask}/>
    {state.collectionLabel&&<div className="composer-attachments" data-knowledge-collection><span className="composer-attachment"><span>{state.collectionLabel}</span><Button size="sm" variant="ghost" isIconOnly aria-label={state.uploadLabels?.remove} onPress={props.onRemoveCollection}>×</Button></span></div>}
    {!!state.attachments?.length&&<div className="composer-attachments" aria-live="polite">{state.attachments.map(file=><div key={file.id} className="composer-attachment" data-upload-id={file.id} data-upload-state={file.status}>
     <ChatAttachment className="composer-attachment-file" mediaType="document" name={file.name} size={file.size_bytes}>
      {file.preview_url?<ChatAttachment.Preview><img className="composer-attachment-preview composer-preview-zoom" role="button" tabIndex={0} src={file.preview_url} alt={file.name} onClick={()=>setZoom({src:file.preview_url!,alt:file.name})} onKeyDown={ev=>{if(ev.key==='Enter'||ev.key===' '){ev.preventDefault();setZoom({src:file.preview_url!,alt:file.name});}}}/></ChatAttachment.Preview>:<ChatAttachment.Preview/>}
      <ChatAttachment.Info><ChatAttachment.Name/>{file.version_label&&<small className="upload-version">{file.version_label}</small>}<span className="upload-state">{file.status==='uploading'?state.uploadLabels?.uploading:file.status==='ready'?state.uploadLabels?.ready:file.status==='error'?file.error:state.uploadLabels?.pending}</span></ChatAttachment.Info>
     </ChatAttachment>
     {file.status==='error'&&<Button size="sm" variant="ghost" className="upload-retry" onPress={()=>props.onRetryUpload(file.id)}>{state.uploadLabels?.retry}</Button>}
     <Button size="sm" variant="ghost" isIconOnly className="upload-remove" isDisabled={file.status==='uploading'} aria-label={`${state.uploadLabels?.remove}: ${file.name}`} onPress={()=>props.onRemoveUpload(file.id)}>×</Button>
    </div>)}</div>}
    {dragging&&<div className="composer-drop-hint">{state.uploadLabels?.drop}</div>}
    {acState&&(acItems.length>0||acState.trigger==='@')&&<div className="composer-ac-menu" role="listbox" aria-label={acState.trigger==='/'?(state.autocompleteLabels?.commandsTitle||'Commands'):(state.autocompleteLabels?.filesTitle||'Files')}>
     <div className="composer-ac-head">{acState.trigger==='/'?(state.autocompleteLabels?.commandsTitle||'Commands'):(state.autocompleteLabels?.filesTitle||'Files')}</div>
     {acItems.length===0?<div className="composer-ac-empty">{state.autocompleteLabels?.empty||'No matching files'}</div>:acItems.map((item,idx)=>{
      const selected=idx===(acState.index%acItems.length);
      return <button key={item.kind+':'+item.id} type="button" role="option" aria-selected={selected} className={'composer-ac-item'+(selected?' selected':'')} onMouseDown={ev=>{ev.preventDefault();applyAutocomplete(item);}}>
       <span className="composer-ac-label">{item.label}</span>
       {item.hint&&<span className="composer-ac-hint">{item.hint}</span>}
      </button>;
     })}
    </div>}
    <PromptInput.Content className="composer-editor"><div className="c-row" onKeyDownCapture={event=>{
     if(acState&&acItems.length>0){
      if(event.key==='ArrowDown'){event.preventDefault();event.stopPropagation();setAcState({...acState,index:(acState.index+1)%acItems.length});return;}
      if(event.key==='ArrowUp'){event.preventDefault();event.stopPropagation();setAcState({...acState,index:(acState.index-1+acItems.length)%acItems.length});return;}
      if((event.key==='Enter'||event.key==='Tab')&&!event.shiftKey&&!event.nativeEvent.isComposing){event.preventDefault();event.stopPropagation();applyAutocomplete(acItems[acState.index%acItems.length]);return;}
     }
     if(acState&&event.key==='Escape'){event.preventDefault();event.stopPropagation();setAcState(null);return;}
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
      disableAutosize onInput={()=>{props.onInput();refreshAutocomplete(input.current);}}/>
    </div></PromptInput.Content>
    <ControlSlot node={props.slots.options}/>
    <PromptInput.Toolbar className="composer-toolbar">
     <PromptInput.ToolbarStart className="composer-toolbar-controls"><PromptInput.Action className="composer-attach" variant="ghost" isIconOnly isDisabled={!state.canAttach} tooltip={state.canAttach?state.uploadLabels?.add:state.uploadLabels?.unavailable} aria-label={state.uploadLabels?.add} onPress={()=>fileInput.current?.click()}>
      <Icon name="plus" size=""/>
     </PromptInput.Action><ControlSlot node={props.slots.tools} className="composer-tools-slot"/></PromptInput.ToolbarStart>
     <PromptInput.ToolbarEnd><div ref={right} className="c-right">
      {state.mode!=='waiting'?<div className="send-group">
       {state.mode==='running'?<PromptInput.Action ref={menu} className="btn ghost xs send-menu-toggle"
        variant="ghost" aria-label={state.menuLabel} title={state.menuLabel}
        onPress={()=>{if(menu.current)props.onMenu(menu.current);}}>
        <Icon name="chevDown" size="xs"/>
       </PromptInput.Action>:null}
       <PromptInput.Send ref={send} className="send" aria-label={state.sendLabel}
        title={state.shortcuts||state.sendLabel} isDisabled={state.disabled} onPress={props.onSend}/>
      </div>:null}
     </div></PromptInput.ToolbarEnd>
    </PromptInput.Toolbar>
    <ControlSlot node={props.slots.help}/>
   </div>
  </PromptInput.Shell>
  {zoom&&<ImageLightbox src={zoom.src} alt={zoom.alt} onClose={()=>setZoom(null)}/>}
 </PromptInput>;
}
