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

// These islands consume the application's real events and existing native controls.
// They own presentation only; task transport, approvals and shortcuts stay in app.js.
import {useLayoutEffect} from 'react';
import {ChainOfThought} from '@heroui-pro/react/chain-of-thought';
import {ChatTool} from '@heroui-pro/react/chat-tool';
import {ChatAttachment,ChatAttachmentGroup} from '@heroui-pro/react/chat-attachment';
import {PromptInput} from '@heroui-pro/react/prompt-input';
import {ChatLoader} from '@heroui-pro/react/chat-loader';

function NativeSlot({node,className}: {node:HTMLElement|null;className?:string}) {
 const ref=useRef<HTMLDivElement>(null);
 useLayoutEffect(()=>{if(ref.current){if(node)ref.current.replaceChildren(node);else ref.current.replaceChildren();}},[node]);
 return <div ref={ref} className={className}/>;
}
type ProgressProps={title:string;current:string;running:boolean;expanded:boolean;onExpandedChange:(value:boolean)=>void;before:HTMLElement|null;steps:Array<{id:string;title:string;detail:string;tool:string;state:string;time:string}>};
function Progress({title,current,running,expanded,onExpandedChange,before,steps}:ProgressProps) {
 return <div className={'worklog pro-worklog'+(expanded?' open':'')} data-pro-component="ChainOfThought">
  <ChainOfThought isExpanded={expanded} onExpandedChange={onExpandedChange} isStreaming={running}>
   <ChainOfThought.Trigger className="worklog-head"><span className="execution-summary">{title}</span></ChainOfThought.Trigger>
   <ChainOfThought.Content className="execution-content">
    <NativeSlot node={before}/>
    <ChainOfThought.Steps className="worklog-steps">
     {steps.map(step=>step.tool?<ChatTool key={step.id} className="execution-tool" data-execution-step="" state={step.state as any} toolName={step.tool} defaultExpanded={false}>
       <ChatTool.Trigger><ChatTool.StatusIcon/><span className="execution-label">{step.title}</span><time>{step.time}</time></ChatTool.Trigger>
       <ChatTool.Content><p className="execution-detail">{step.detail || step.title}</p></ChatTool.Content>
      </ChatTool>:<ChainOfThought.Step key={step.id} data-execution-step="" label={<span className="execution-step-title"><span className="execution-label">{step.title}</span><time>{step.time}</time></span>}>
        {step.detail?<p className="execution-detail">{step.detail}</p>:null}
      </ChainOfThought.Step>)}
    </ChainOfThought.Steps>
   </ChainOfThought.Content>
  </ChainOfThought>
  {running&&current?<div className="execution-current" role="status"><ChatLoader.Dots aria-hidden="true"/><span data-current-label="">{current}</span></div>:null}
 </div>;
}
function Attachments({files,downloadLabel,expiredLabel}:{files:Array<{name:string;size_bytes?:number;url?:string;mime_type?:string}>;downloadLabel:string;expiredLabel:string}) {
 return <ChatAttachmentGroup className="delivered-attachments" data-pro-component="ChatAttachment">
  {files.map((file,i)=>{const url=safeHref(file.url);const content=<ChatAttachment mediaType="document" name={file.name} size={file.size_bytes} mimeType={file.mime_type}>
    <ChatAttachment.Preview/><ChatAttachment.Info/>
   </ChatAttachment>;
   return url?<a key={i} href={url} download={file.name} aria-label={`${downloadLabel}: ${file.name}`}>{content}</a>:<div key={i} className="attachment-expired" title={expiredLabel}>{content}<small>{expiredLabel}</small></div>;
  })}
 </ChatAttachmentGroup>;
}
function Composer({body,status,onReady}:{body:HTMLElement;status:'ready'|'streaming'|'submitted';onReady?:()=>void}) {
 useLayoutEffect(()=>{onReady?.();},[]);
 return <PromptInput layout="stacked" lockInputOnRun={false} status={status} maxHeight={260} className="pro-prompt" data-pro-component="PromptInput">
  <PromptInput.Shell><PromptInput.Content><NativeSlot node={body} className="composer-native-slot"/></PromptInput.Content></PromptInput.Shell>
 </PromptInput>;
}
function mountComponent<P>(container:HTMLElement,Component:React.ComponentType<P>,initial:P) {
 const root=createRoot(container);let disposed=false;container.dataset.proMounted='true';
 const update=(props:P)=>{if(!disposed)root.render(<Component {...props as any}/>);};
 update(initial);return {update,unmount:()=>{if(!disposed){disposed=true;root.unmount();delete container.dataset.proMounted;}}};
}
export const mountProgress=(container:HTMLElement,props:ProgressProps)=>mountComponent(container,Progress,props);
export const mountAttachments=(container:HTMLElement,props:React.ComponentProps<typeof Attachments>)=>mountComponent(container,Attachments,props);
export function mountComposer(container:HTMLElement,props:React.ComponentProps<typeof Composer>) {
 const focused=document.activeElement instanceof HTMLTextAreaElement&&container.contains(document.activeElement)?document.activeElement:null;
 const selection=focused?[focused.selectionStart,focused.selectionEnd]:null;
 return mountComponent(container,Composer,{...props,onReady:()=>{if(focused?.isConnected&&document.activeElement===document.body){focused.focus();if(selection)focused.setSelectionRange(selection[0],selection[1]);}}});
}
