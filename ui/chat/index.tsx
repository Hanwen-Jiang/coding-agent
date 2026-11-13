import React, {Component, useEffect, useRef, useState, type ReactNode} from 'react';
import {createRoot, type Root} from 'react-dom/client';
import {ChatMessage} from '@heroui-pro/react/chat-message';
import {Markdown} from '@heroui-pro/react/markdown';
import {CodeBlock} from '@heroui-pro/react/code-block';
import type {Components} from 'react-markdown';

export interface AnswerProps {
 id: string;
 text: string;
 streaming: boolean;
 copyLabel: string;
 copiedLabel: string;
 copyFailedLabel: string;
}
export interface AnswerHandle {
 update(props: AnswerProps): void;
 unmount(): void;
}
class AnswerBoundary extends Component<{text: string; children: ReactNode}, {failed:boolean}> {
 state = {failed:false};
 static getDerivedStateFromError() {return {failed:true};}
 render() {return this.state.failed ? <div className="pro-answer-fallback">{this.props.text}</div> : this.props.children;}
}
function safeHref(value?: string): string | undefined {
 if (!value) return undefined;
 try {const parsed = new URL(value,location.href); return ['https:','http:','mailto:'].includes(parsed.protocol) ? parsed.href : undefined;} catch {return undefined;}
}
function CopyCode({code,label,copiedLabel,failedLabel}: {code:string;label:string;copiedLabel:string;failedLabel:string}) {
 const [state,setState] = useState('ready');
 const timer=useRef<ReturnType<typeof setTimeout> | null>(null);
 useEffect(()=>()=>{if(timer.current)clearTimeout(timer.current);},[]);
 return <button type="button" className="btn ghost xs code-block__copy-button" data-slot="code-block-copy-button" aria-label={label} aria-live="polite" onClick={async()=>{
  try {await navigator.clipboard.writeText(code);setState('copied');} catch {setState('failed');}
  if(timer.current)clearTimeout(timer.current);timer.current=setTimeout(()=>setState('ready'),2000);
 }}>{state==='copied'?copiedLabel:state==='failed'?failedLabel:label}</button>;
}
function Answer({id,text,streaming,copyLabel,copiedLabel,copyFailedLabel}: AnswerProps) {
 const components = React.useMemo<Components>(() => ({
  a: ({href,children}) => <a href={safeHref(href)} target="_blank" rel="noopener noreferrer">{children}</a>,
  img: ({src,alt}) => <img src={safeHref(src)} alt={alt || ''} loading="lazy" referrerPolicy="no-referrer" />,
  code: ({children,className,node}) => {
   if (node?.position?.start.line === node?.position?.end.line) return <code>{children}</code>;
   const language = /language-([\w+-]+)/.exec(className || '')?.[1] || 'plaintext';
   const code = String(children || '').replace(/\n$/,'');
   return <CodeBlock><CodeBlock.Header><span>{language}</span><CopyCode code={code} label={copyLabel} copiedLabel={copiedLabel} failedLabel={copyFailedLabel}/></CodeBlock.Header><CodeBlock.Code code={code} language={language}/></CodeBlock>;
  },
 }), [copyLabel,copiedLabel,copyFailedLabel]);
 return <AnswerBoundary text={text}><ChatMessage.Assistant data-pro-component="ChatMessage" data-streaming={String(streaming)}>
  <ChatMessage.Body><ChatMessage.Content><Markdown id={id} components={components}>{text}</Markdown></ChatMessage.Content></ChatMessage.Body>
 </ChatMessage.Assistant></AnswerBoundary>;
}
export function mountAnswer(container: HTMLElement, initial: AnswerProps): AnswerHandle {
 const root: Root = createRoot(container);
 let disposed = false;
 container.dataset.proMounted = 'true';
 const update = (props: AnswerProps) => {if (!disposed) root.render(<Answer {...props}/>);};
 const unmount = () => {if (!disposed) {disposed=true;root.unmount();delete container.dataset.proMounted;}};
 update(initial);
 return {update,unmount};
}

import {ChatAttachment,ChatAttachmentGroup} from '@heroui-pro/react/chat-attachment';
import {Composer, type ComposerProps} from './composer';
import {Conversation, type ConversationProps} from './conversation';

function Attachments({files,downloadLabel,expiredLabel}:{files:Array<{name:string;size_bytes?:number;url?:string;mime_type?:string}>;downloadLabel:string;expiredLabel:string}) {
 return <ChatAttachmentGroup className="delivered-attachments" data-pro-component="ChatAttachment">
  {files.map((file,i)=>{const url=safeHref(file.url);const content=<ChatAttachment mediaType="document" name={file.name} size={file.size_bytes} mimeType={file.mime_type}>
    <ChatAttachment.Preview/><ChatAttachment.Info/>
   </ChatAttachment>;
   return url?<a key={i} href={url} download={file.name} aria-label={`${downloadLabel}: ${file.name}`}>{content}</a>:<div key={i} className="attachment-expired" title={expiredLabel}>{content}<small>{expiredLabel}</small></div>;
  })}
 </ChatAttachmentGroup>;
}
function mountComponent<P>(container:HTMLElement,Component:React.ComponentType<P>,initial:P) {
 const root=createRoot(container);let disposed=false;container.dataset.proMounted='true';
 const update=(props:P)=>{if(!disposed)root.render(<Component {...props as any}/>);};
 update(initial);return {update,unmount:()=>{if(!disposed){disposed=true;root.unmount();delete container.dataset.proMounted;}}};
}
export const mountAttachments=(container:HTMLElement,props:React.ComponentProps<typeof Attachments>)=>mountComponent(container,Attachments,props);
export function mountComposer(container:HTMLElement,props:ComposerProps) {
 return mountComponent(container,Composer,props);
}

export const mountConversation=(container:HTMLElement,props:ConversationProps)=>mountComponent(container,Conversation,props);

import {RunDetails, type RunDetailsProps} from "./run-details";
export const mountRunDetails=(container:HTMLElement,props:RunDetailsProps)=>mountComponent(container,RunDetails,props);

import {ContextPanel,type ContextPanelProps} from "./context-panel";
export const mountContextPanel=(container:HTMLElement,props:ContextPanelProps)=>mountComponent(container,ContextPanel,props);

import {AppSidebar,type SidebarProps,type SidebarHandle} from './sidebar';
// The sidebar keeps one React root for the whole signed-in session; the controller re-attaches its host on every route change.
export function mountSidebar(container:HTMLElement,initial:SidebarProps):SidebarHandle {
 const root=createRoot(container);let disposed=false;const api:{current:any}={current:null};
 container.dataset.proMounted='true';
 const update=(props:SidebarProps)=>{if(!disposed)root.render(<AppSidebar {...props} api={api}/>);};
 update(initial);
 return {update,unmount:()=>{if(!disposed){disposed=true;root.unmount();delete container.dataset.proMounted;}},openMobile:()=>api.current?.setMobileOpen(true)};
}

import {CommandPalette,type CommandProps} from './command';
export const mountCommand=(container:HTMLElement,props:CommandProps)=>mountComponent(container,CommandPalette,props);

import {Dialogs,type DialogsProps} from './dialogs';
export const mountDialogs=(container:HTMLElement,props:DialogsProps)=>mountComponent(container,Dialogs,props);

import {FileWorkbench,type FileWorkbenchProps} from './file-workbench';
export const mountFileWorkbench=(container:HTMLElement,props:FileWorkbenchProps)=>mountComponent(container,FileWorkbench,props);
