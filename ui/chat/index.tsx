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
