import React, {useState} from 'react';
import {Button, Disclosure} from '@heroui/react';
import {ChatAttachment} from '@heroui-pro/react/chat-attachment';

export type ContextAgent = {id:string;name:string;state:string;detail?:string};
export type ContextSource = {id:string;name:string;size_bytes?:number;mime_type?:string;url?:string;preview_url?:string};
export interface ContextPanelProps {
 agents:ContextAgent[];
 sources:ContextSource[];
 sourcesLoading?:boolean;
 sourcesError?:string;
 uploading?:boolean;
 readOnly?:boolean;
 onAdd:()=>void;
 onOpenSource:(source:ContextSource)=>void;
 onUseSource?:(source:ContextSource)=>void;
 onRetry?:()=>void;
 labels:{
  agents:string;sources:string;running:string;completed:string;failed:string;cancelled:string;unknown:string;
  uploaded:string;addSource:string;viewAll:string;showLess:string;noAgents:string;noSources:string;
  loading:string;retry:string;readOnly:string;uploading:string;openSource:string;useSource:string;unavailable:string;
 };
}

function PlusIcon() {
 return <svg viewBox="0 0 20 20" width="16" height="16" fill="none" aria-hidden="true"><path d="M10 4v12M4 10h12" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/></svg>;
}
function AgentIcon() {
 return <svg viewBox="0 0 20 20" width="17" height="17" fill="none" aria-hidden="true"><circle cx="10" cy="5" r="2" stroke="currentColor" strokeWidth="1.3"/><circle cx="5" cy="14" r="2" stroke="currentColor" strokeWidth="1.3"/><circle cx="15" cy="14" r="2" stroke="currentColor" strokeWidth="1.3"/><path d="M10 7v3M5 12v-2h10v2" stroke="currentColor" strokeWidth="1.3"/></svg>;
}

function agentState(state:string):'running'|'completed'|'failed'|'cancelled'|'unknown' {
 if(state==='running')return 'running';
 if(['succeeded','completed','done'].includes(state))return 'completed';
 if(['failed','error'].includes(state))return 'failed';
 if(['cancelled','canceled','aborted'].includes(state))return 'cancelled';
 return 'unknown';
}

function AgentRow({agent,labels}:{agent:ContextAgent;labels:ContextPanelProps['labels']}) {
 const state=agentState(agent.state);
 const [expanded,setExpanded]=useState(false);
 const content=<><span className="context-agent-dot" data-state={state}/><span className="context-agent-name" title={agent.name}>{agent.name}</span><span className="context-agent-state">{labels[state]}</span></>;
 if(!agent.detail)return <li className="context-agent-item" data-agent-id={agent.id} data-state={state}><div className="context-agent-row">{content}</div></li>;
 return <li className="context-agent-item" data-agent-id={agent.id} data-state={state}>
  <Disclosure isExpanded={expanded} onExpandedChange={setExpanded}>
   <Disclosure.Heading><Button slot="trigger" className="context-agent-row" variant="ghost" size="sm" aria-label={`${agent.name} · ${labels[state]}`}>
    {content}<Disclosure.Indicator/>
   </Button></Disclosure.Heading>
   <Disclosure.Content><Disclosure.Body><p className="context-agent-detail">{agent.detail}</p></Disclosure.Body></Disclosure.Content>
  </Disclosure>
 </li>;
}

// Uploaded previews are served by the application. External links may still be
// opened by the caller, but cannot start a background image request in this panel.
function previewURL(value?:string):string|undefined {
 if(!value)return undefined;
 try {const url=new URL(value,location.href);return ['http:','https:'].includes(url.protocol)&&url.origin===location.origin?url.href:undefined;}catch{return undefined;}
}

function SourceRow({source,onOpen,onUse,disabled,openLabel,useLabel,unavailableLabel}:{source:ContextSource;onOpen:(source:ContextSource)=>void;onUse?:(source:ContextSource)=>void;disabled:boolean;openLabel:string;useLabel:string;unavailableLabel:string}) {
 const [failedURL,setFailedURL]=useState<string>();
 const url=previewURL(source.preview_url);
 const thumbnail=/^image\/(png|jpeg|gif|webp|avif|bmp)$/i.test(source.mime_type||'')&&url&&failedURL!==url?url:undefined;
 let available=false;
 try {available=!!source.url&&['http:','https:'].includes(new URL(source.url,location.href).protocol);}catch{/* Keep malformed links visible, without activating them. */}
 return <li className="context-source-item" data-source-id={source.id}>
  <Button variant="ghost" className="context-source-open" fullWidth isDisabled={!available} onPress={()=>onOpen(source)} aria-label={`${available?openLabel:unavailableLabel}: ${source.name}`} title={available?source.name:unavailableLabel}>
   <ChatAttachment className="context-source-file" mediaType="document" name={source.name} mimeType={source.mime_type} size={source.size_bytes}>
    {thumbnail?<ChatAttachment.Preview><span className="context-source-preview"><img src={thumbnail} alt="" loading="lazy" referrerPolicy="no-referrer" onError={()=>setFailedURL(thumbnail)}/></span></ChatAttachment.Preview>:<ChatAttachment.Preview/>}
    <ChatAttachment.Info><ChatAttachment.Name/><ChatAttachment.Size/></ChatAttachment.Info>
   </ChatAttachment>
  </Button>
  {onUse&&<Button className="context-source-use" variant="ghost" size="sm" isIconOnly isDisabled={disabled||!available} aria-label={`${useLabel}: ${source.name}`} title={useLabel} onPress={()=>onUse(source)}><PlusIcon/></Button>}
 </li>;
}

export function ContextPanel({agents,sources,sourcesLoading=false,sourcesError='',uploading=false,readOnly=false,onAdd,onOpenSource,onUseSource,onRetry,labels}:ContextPanelProps) {
 const [agentsExpanded,setAgentsExpanded]=useState(true);
 const [allSources,setAllSources]=useState(false);
 const running=agents.filter(agent=>agentState(agent.state)==='running').length;
 const completed=agents.filter(agent=>agentState(agent.state)==='completed').length;
 const failed=agents.filter(agent=>agentState(agent.state)==='failed').length;
 const cancelled=agents.filter(agent=>agentState(agent.state)==='cancelled').length;
 const visibleSources=allSources?sources:sources.slice(0,3);
 return <div className="context-panel" data-pro-component="ContextPanel">
  <section className="context-section context-agents" aria-label={labels.agents}>
   <Disclosure isExpanded={agentsExpanded} onExpandedChange={setAgentsExpanded}>
    <Disclosure.Heading><Button slot="trigger" className="context-section-trigger" variant="ghost" size="sm">
     <AgentIcon/><span className="context-section-title">{labels.agents}</span><Disclosure.Indicator/>
    </Button></Disclosure.Heading>
    <div className="context-agent-counts" aria-live="polite" aria-atomic="true">
     <span data-agent-count="running"><strong>{running}</strong> {labels.running}</span><span data-agent-count="completed"><strong>{completed}</strong> {labels.completed}</span>
     {failed>0&&<span data-agent-count="failed"><strong>{failed}</strong> {labels.failed}</span>}{cancelled>0&&<span data-agent-count="cancelled"><strong>{cancelled}</strong> {labels.cancelled}</span>}
    </div>
    <Disclosure.Content><Disclosure.Body>
     {agents.length?<ul className="context-agent-list">{agents.map(agent=><AgentRow key={agent.id} agent={agent} labels={labels}/>)}</ul>:<p className="context-empty">{labels.noAgents}</p>}
    </Disclosure.Body></Disclosure.Content>
   </Disclosure>
  </section>
  <section className="context-section context-sources" aria-label={labels.sources} aria-busy={sourcesLoading||uploading}>
   <div className="context-section-head"><h3 className="context-section-title">{labels.sources}{sources.length>0&&<span className="context-source-count">{sources.length}</span>}</h3>
    <Button className="context-source-add" variant="ghost" size="sm" isIconOnly isDisabled={readOnly||uploading} onPress={onAdd} aria-label={labels.addSource} title={readOnly?labels.readOnly:uploading?labels.uploading:labels.addSource}><PlusIcon/></Button>
   </div>
   <p className="context-section-caption">{labels.uploaded}</p>
   {(sourcesLoading||uploading)&&<p className="context-loading" role="status">{uploading?labels.uploading:labels.loading}</p>}
   {sourcesError&&<div className="context-error" role="alert"><p>{sourcesError}</p>{onRetry&&<Button className="context-retry" variant="ghost" size="sm" onPress={onRetry} isDisabled={sourcesLoading}>{labels.retry}</Button>}</div>}
   {sources.length?<ul className="context-source-list">{visibleSources.map(source=><SourceRow key={source.id} source={source} onOpen={onOpenSource} onUse={onUseSource} disabled={readOnly||uploading} useLabel={labels.useSource} openLabel={labels.openSource} unavailableLabel={labels.unavailable}/>)}</ul>:!sourcesLoading&&!sourcesError&&<p className="context-empty">{labels.noSources}</p>}
   {sources.length>3&&<Button className="context-sources-more" variant="ghost" size="sm" onPress={()=>setAllSources(!allSources)} aria-expanded={allSources}>{allSources?labels.showLess:labels.viewAll.replace('{n}',String(sources.length))}</Button>}
   {readOnly&&<p className="context-readonly">{labels.readOnly}</p>}
  </section>
 </div>;
}
