import React, {useEffect,useRef,useState} from 'react';
import {Button} from '@heroui/react';
import {CodeBlock} from '@heroui-pro/react/code-block';

type Entry={id:string;title:string;detail:string;time:string;kind:string};
export type RunDetailsProps={title:string;summary:string;current:string;entries:Entry[];raw:string;metrics:Array<{id:string;label:string;value:number}>;labels:{events:string;raw:string;more:string;less:string;copy:string;copied:string;copyFailed:string}};
function Chevron(){return <svg className="diagnostic-chevron" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="m6 4 4 4-4 4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round"/></svg>;}
export function RunDetails({title,summary,current,entries,raw,metrics,labels}:RunDetailsProps){
 const [all,setAll]=useState(false),[copied,setCopied]=useState('');
 const timer=useRef<ReturnType<typeof setTimeout>|null>(null);
 useEffect(()=>()=>{if(timer.current)clearTimeout(timer.current);},[]);
 const copy=async()=>{try{await navigator.clipboard.writeText(raw);setCopied(labels.copied);}catch{setCopied(labels.copyFailed);}if(timer.current)clearTimeout(timer.current);timer.current=setTimeout(()=>setCopied(''),2000);};
 return <details className="run-diagnostics" data-pro-component="RunDetails">
  <summary className="diagnostic-trigger"><svg className="diagnostic-inspect-icon" viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d="M2 4h12M2 8h12M2 12h12M5 2v4M11 6v4M6 10v4" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round"/></svg><span>{title}</span><span className="diagnostic-summary">{summary}</span><Chevron/></summary>
  <div className="run-diagnostics-body">
   <dl className="diagnostic-metrics">{metrics.map(metric=><div key={metric.id} className="diagnostic-metric" data-metric={metric.id} data-value={metric.value}><dt>{metric.label}</dt><dd className="diagnostic-metric-value">{metric.value}</dd></div>)}</dl>
   {current&&<div className="diagnostic-current"><span className="diagnostic-dot" data-kind="run"/>{current}</div>}
   <div className="diagnostic-section-title">{labels.events}</div>
   <div className="diagnostic-events">{entries.map((entry,index)=>{
    const heading=<><span className="diagnostic-dot" data-kind={entry.kind}/><strong title={entry.title}>{entry.title}</strong><time>{entry.time}</time>{entry.detail&&<Chevron/>}</>;
    return entry.detail?<details key={entry.id} className="diagnostic-event" hidden={!all&&index<entries.length-6}>
     <summary className="diagnostic-event-title">{heading}</summary><p className="diagnostic-event-detail">{entry.detail}</p>
    </details>:<div key={entry.id} className="diagnostic-event" hidden={!all&&index<entries.length-6}><div className="diagnostic-event-title">{heading}</div></div>;
   })}</div>
   {entries.length>6&&<Button className="diagnostic-more" variant="ghost" size="sm" onPress={()=>setAll(!all)}>{all?labels.less:labels.more.replace('{n}',String(entries.length-6))}</Button>}
   <details className="execution-raw"><summary><span className="diagnostic-json-icon" aria-hidden="true">{'{}'}</span>{labels.raw}<Chevron/></summary>
    <CodeBlock className="diagnostic-code"><CodeBlock.Header><span>JSON</span><Button variant="ghost" size="sm" onPress={copy} aria-label={labels.copy}>{copied||labels.copy}</Button></CodeBlock.Header><pre tabIndex={0} aria-label={labels.raw}>{raw}</pre></CodeBlock>
   </details>
  </div>
 </details>;
}
