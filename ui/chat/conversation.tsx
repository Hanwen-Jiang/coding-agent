import React, {useEffect,useRef,useState} from 'react';
import {Button} from '@heroui/react';
import {ChainOfThought} from '@heroui-pro/react/chain-of-thought';
import {ChatTool} from '@heroui-pro/react/chat-tool';
import {ChatMessage} from '@heroui-pro/react/chat-message';
import {Markdown} from '@heroui-pro/react/markdown';
import {ChatLoader} from '@heroui-pro/react/chat-loader';

type Step = {id:string;label:string;target:string;detail:string;error:string;tool:string;state:'input-available'|'output-error'|'output-available';time:string;duration:string};
type Plan = {title:string;agentsTitle:string;steps:Array<{id:string;title:string;status:string;statusLabel:string;current:boolean}>;agents:Array<{id:string;name:string;state:string;statusLabel:string;label:string;last:string}>};
type Group = {kind:'tools';id:string;reveal?:number;title:string;current:string;running:boolean;steps:Step[];plan?:Plan};
type Note = {kind:'progress';id:string;message:string;source:string};
export type ConversationProps = {parts:Array<Group|Note>;labels:{more:string;less:string;command:string;target:string;failed:string;plan:string}};

function publicHref(href?:string) {
 try {const url=new URL(href || '',location.href);return href&&['http:','https:','mailto:'].includes(url.protocol)?url.href:undefined;}catch{return undefined;}
}
function ProgressNote({part}:{part:Note}) {
 return <ChatMessage.Assistant className="progress-message" data-public-progress={part.source}>
  <ChatMessage.Body><ChatMessage.Content><Markdown id={'progress-'+part.id} components={{
   a:({href,children})=><a href={publicHref(href)} target="_blank" rel="noopener noreferrer">{children}</a>,
   img:({alt})=><span>{alt}</span>,
  }}>{part.message}</Markdown></ChatMessage.Content></ChatMessage.Body>
 </ChatMessage.Assistant>;
}
function animateOnce(element:HTMLElement|null,keyframes:Keyframe[],duration:number) {
 const preference=matchMedia('(prefers-reduced-motion: reduce)');
 const animation=preference.matches?undefined:element?.animate(keyframes,{duration,easing:'cubic-bezier(.2,.7,.2,1)'});
 const stopMotion=()=>{if(preference.matches)animation?.cancel();};
 preference.addEventListener('change',stopMotion);
 return()=>{animation?.cancel();preference.removeEventListener('change',stopMotion);};
}
function ProgressParagraph({part}:{part:Note}) {
 const element=useRef<HTMLDivElement>(null);
 // Animate only on React mount; SSE can move this same host without replaying.
 useEffect(()=>animateOnce(element.current,[
  {opacity:0,transform:'translateY(5px)'},{opacity:1,transform:'translateY(0)'},
 ],220),[]);
 return <div ref={element} className="progress-note-island"><ProgressNote part={part}/></div>;
}
function PlanView({plan}:{plan:Plan}) {
 if(!plan.steps.length&&!plan.agents.length)return null;
 return <div className="execution-plan">
  {plan.steps.length>0&&<div className="plan"><div className="plan-title">{plan.title}</div><ol className="plan-steps">
   {plan.steps.map(step=><li key={step.id} className={'plan-step'+(step.current?' current':'')} data-status={step.status} title={step.statusLabel} aria-label={`${step.title} · ${step.statusLabel}`}><span className="plan-mark"/><span className="plan-text">{step.title}</span></li>)}
  </ol></div>}
  {plan.agents.length>0&&<div className="subagents"><div className="subagents-title">{plan.agentsTitle}</div>{plan.agents.map(agent=><div key={agent.id} className="subagent" data-node={agent.name} data-state={agent.state} title={agent.statusLabel} aria-label={`${agent.name} · ${agent.statusLabel}`}>
   <span className="subagent-mark"/><span className="subagent-name">{agent.name}</span><span className="subagent-steps muted">{agent.label}</span>{agent.last&&<span className="subagent-last">{agent.last}</span>}
  </div>)}</div>}
 </div>;
}
function ToolRow({step,labels}:{step:Step;labels:ConversationProps['labels']}) {
 const previous=useRef(step.state);
 const status=useRef<HTMLSpanElement>(null);
 const [settled,setSettled]=useState(false);
 useEffect(()=>{
  const completed=previous.current==='input-available'&&step.state!=='input-available';
  previous.current=step.state;
  if(!completed)return;
  setSettled(true);
  // An explicit Animation belongs to this state transition, so reparenting the
  // React host during SSE cannot restart it as it can a CSS animation.
  const dispose=animateOnce(status.current,[
   {opacity:.45,transform:'scale(.8)'},
   {opacity:1,transform:'scale(1.12)',offset:.55},
   {opacity:1,transform:'scale(1)'},
  ],280);
  const timer=setTimeout(()=>setSettled(false),320);
  return()=>{clearTimeout(timer);dispose();};
 },[step.state]);
 return <ChatTool className="execution-tool" data-execution-step="" data-call-id={step.id} state={step.state} active={false} toolName={step.tool} defaultExpanded={false}>
  <ChatTool.Trigger aria-label={step.state==='output-error'?`${labels.failed}: ${step.label} ${step.target}`:undefined}>
   <span className="execution-row"><span ref={status} className="execution-status" data-settled={settled}><ChatTool.StatusIcon/></span><span className="execution-label">{step.label}</span>
    {step.target&&<span className="execution-target" title={step.target}>{step.target}</span>}
    {step.duration&&<span className="execution-duration">{step.duration}</span>}
   </span>
  </ChatTool.Trigger>
  <ChatTool.Content>
   {step.target&&<ChatTool.Args label={step.tool==='shell_run'?labels.command:labels.target}><pre className="execution-code">{step.target}</pre></ChatTool.Args>}
   {step.error&&<ChatTool.Error label={labels.failed} errorText={step.error}/>}
   {(step.detail||step.time)&&<div className="execution-meta">{step.detail&&<span>{step.detail}</span>}<time>{step.time}</time></div>}
  </ChatTool.Content>
 </ChatTool>;
}
function ToolGroup({part,labels}:{part:Group;labels:ConversationProps['labels']}) {
 const [expanded,setExpanded]=useState(false);
 const [all,setAll]=useState(false);
 useEffect(()=>{if(part.reveal)setExpanded(true);},[part.reveal]);
 // The group follows document scrolling. Large histories disclose more rows,
 // never create another scroll viewport inside the conversation.
 const visible=all?part.steps:part.steps.slice(0,8);
 const failures=part.steps.filter(step=>step.state==='output-error');
 return <div className="progress-island">
  <div className={'worklog pro-worklog'+(expanded?' open':'')} data-pro-component="ChainOfThought">
   <ChainOfThought isExpanded={expanded} onExpandedChange={setExpanded}>
    <ChainOfThought.Trigger className="worklog-head"><span className="execution-summary">{part.title}</span>
     {failures.length>0&&<span className="execution-failures">{labels.failed} {failures.length}</span>}
    </ChainOfThought.Trigger>
    <ChainOfThought.Content className="execution-content">
     {part.plan&&<PlanView plan={part.plan}/>}
     {part.steps.length>0&&<div className="worklog-steps">{visible.map(step=><ToolRow key={step.id} step={step} labels={labels}/>)}</div>}
     {part.steps.length>8&&<Button className="execution-more" size="sm" variant="ghost" onPress={()=>setAll(!all)}>{all?labels.less:labels.more.replace('{n}',String(part.steps.length-8))}</Button>}
    </ChainOfThought.Content>
   </ChainOfThought>
   {part.running&&part.current&&<div className="execution-current" role="status"><ChatLoader.Dots aria-hidden="true"/><span data-current-label="">{part.current}</span></div>}
  </div>
 </div>;
}
export function Conversation({parts,labels}:ConversationProps) {
 return <div className="conversation-flow" data-pro-component="Conversation">{parts.map(part=>part.kind==='progress'
  ?<ProgressParagraph key={part.id} part={part}/>
  :<ToolGroup key={part.id} part={part} labels={labels}/>)}</div>;
}
