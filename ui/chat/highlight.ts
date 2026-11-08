// A finite Shiki language set avoids bundling every grammar or loading remote code.
import {createHighlighterCore} from '@shikijs/core';
import {createJavaScriptRegexEngine} from '@shikijs/engine-javascript';
import light from '@shikijs/themes/github-light';
import dark from '@shikijs/themes/github-dark';
import javascript from '@shikijs/langs/javascript';
import typescript from '@shikijs/langs/typescript';
import jsx from '@shikijs/langs/jsx';
import tsx from '@shikijs/langs/tsx';
import python from '@shikijs/langs/python';
import json from '@shikijs/langs/json';
import bash from '@shikijs/langs/bash';
import go from '@shikijs/langs/go';
import sql from '@shikijs/langs/sql';
import yaml from '@shikijs/langs/yaml';
import html from '@shikijs/langs/html';
import css from '@shikijs/langs/css';
import diff from '@shikijs/langs/diff';
const highlighter = createHighlighterCore({themes:[light,dark],langs:[javascript,typescript,jsx,tsx,python,json,bash,go,sql,yaml,html,css,diff],engine:createJavaScriptRegexEngine()});
export async function codeToTokens(code: string, options: Record<string, unknown>) {
 const instance = await highlighter;
 const aliases: Record<string,string> = {js:'javascript',ts:'typescript',py:'python',sh:'bash',shell:'bash',yml:'yaml',text:'plaintext',txt:'plaintext'};
 const requested = String(options.lang || 'plaintext').toLowerCase();
 const name = aliases[requested] || requested;
 const lang = instance.getLoadedLanguages().includes(name) ? name : 'plaintext';
 return instance.codeToTokens(code,{...options,lang} as Parameters<typeof instance.codeToTokens>[1]);
}
