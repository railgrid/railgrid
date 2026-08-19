(function(){"use strict";/**
* @vue/shared v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/function Xn(e){const t=Object.create(null);for(const n of e.split(","))t[n]=1;return n=>n in t}const Z={},Tt=[],Le=()=>{},zs=()=>!1,cn=e=>e.charCodeAt(0)===111&&e.charCodeAt(1)===110&&(e.charCodeAt(2)>122||e.charCodeAt(2)<97),un=e=>e.startsWith("onUpdate:"),de=Object.assign,Qn=(e,t)=>{const n=e.indexOf(t);n>-1&&e.splice(n,1)},ti=Object.prototype.hasOwnProperty,J=(e,t)=>ti.call(e,t),P=Array.isArray,$t=e=>Vt(e)==="[object Map]",fn=e=>Vt(e)==="[object Set]",Ws=e=>Vt(e)==="[object Date]",F=e=>typeof e=="function",re=e=>typeof e=="string",Pe=e=>typeof e=="symbol",Y=e=>e!==null&&typeof e=="object",Gs=e=>(Y(e)||F(e))&&F(e.then)&&F(e.catch),qs=Object.prototype.toString,Vt=e=>qs.call(e),ni=e=>Vt(e).slice(8,-1),Js=e=>Vt(e)==="[object Object]",Zn=e=>re(e)&&e!=="NaN"&&e[0]!=="-"&&""+parseInt(e,10)===e,Ft=Xn(",key,ref,ref_for,ref_key,onVnodeBeforeMount,onVnodeMounted,onVnodeBeforeUpdate,onVnodeUpdated,onVnodeBeforeUnmount,onVnodeUnmounted"),dn=e=>{const t=Object.create(null);return(n=>t[n]||(t[n]=e(n)))},si=/-\w/g,Ne=dn(e=>e.replace(si,t=>t.slice(1).toUpperCase())),ri=/\B([A-Z])/g,pt=dn(e=>e.replace(ri,"-$1").toLowerCase()),Ys=dn(e=>e.charAt(0).toUpperCase()+e.slice(1)),es=dn(e=>e?`on${Ys(e)}`:""),Ke=(e,t)=>!Object.is(e,t),pn=(e,...t)=>{for(let n=0;n<e.length;n++)e[n](...t)},Xs=(e,t,n,s=!1)=>{Object.defineProperty(e,t,{configurable:!0,enumerable:!1,writable:s,value:n})},hn=e=>{const t=parseFloat(e);return isNaN(t)?e:t};let Qs;const gn=()=>Qs||(Qs=typeof globalThis<"u"?globalThis:typeof self<"u"?self:typeof window<"u"?window:typeof global<"u"?global:{});function mn(e){if(P(e)){const t={};for(let n=0;n<e.length;n++){const s=e[n],r=re(s)?ai(s):mn(s);if(r)for(const o in r)t[o]=r[o]}return t}else if(re(e)||Y(e))return e}const oi=/;(?![^(]*\))/g,ii=/:([^]+)/,li=/\/\*[^]*?\*\//g;function ai(e){const t={};return e.replace(li,"").split(oi).forEach(n=>{if(n){const s=n.split(ii);s.length>1&&(t[s[0].trim()]=s[1].trim())}}),t}function Ie(e){let t="";if(re(e))t=e;else if(P(e))for(let n=0;n<e.length;n++){const s=Ie(e[n]);s&&(t+=s+" ")}else if(Y(e))for(const n in e)e[n]&&(t+=n+" ");return t.trim()}const ci=Xn("itemscope,allowfullscreen,formnovalidate,ismap,nomodule,novalidate,readonly");function Zs(e){return!!e||e===""}function ui(e,t){if(e.length!==t.length)return!1;let n=!0;for(let s=0;n&&s<e.length;s++)n=Dt(e[s],t[s]);return n}function Dt(e,t){if(e===t)return!0;let n=Ws(e),s=Ws(t);if(n||s)return n&&s?e.getTime()===t.getTime():!1;if(n=Pe(e),s=Pe(t),n||s)return e===t;if(n=P(e),s=P(t),n||s)return n&&s?ui(e,t):!1;if(n=Y(e),s=Y(t),n||s){if(!n||!s)return!1;const r=Object.keys(e).length,o=Object.keys(t).length;if(r!==o)return!1;for(const i in e){const l=e.hasOwnProperty(i),a=t.hasOwnProperty(i);if(l&&!a||!l&&a||!Dt(e[i],t[i]))return!1}}return String(e)===String(t)}function fi(e,t){return e.findIndex(n=>Dt(n,t))}const er=e=>!!(e&&e.__v_isRef===!0),H=e=>re(e)?e:e==null?"":P(e)||Y(e)&&(e.toString===qs||!F(e.toString))?er(e)?H(e.value):JSON.stringify(e,tr,2):String(e),tr=(e,t)=>er(t)?tr(e,t.value):$t(t)?{[`Map(${t.size})`]:[...t.entries()].reduce((n,[s,r],o)=>(n[ts(s,o)+" =>"]=r,n),{})}:fn(t)?{[`Set(${t.size})`]:[...t.values()].map(n=>ts(n))}:Pe(t)?ts(t):Y(t)&&!P(t)&&!Js(t)?String(t):t,ts=(e,t="")=>{var n;return Pe(e)?`Symbol(${(n=e.description)!=null?n:t})`:e};/**
* @vue/reactivity v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/let pe;class di{constructor(t=!1){this.detached=t,this._active=!0,this._on=0,this.effects=[],this.cleanups=[],this._isPaused=!1,this._warnOnRun=!0,this.__v_skip=!0,!t&&pe&&(pe.active?(this.parent=pe,this.index=(pe.scopes||(pe.scopes=[])).push(this)-1):(this._active=!1,this._warnOnRun=!1))}get active(){return this._active}pause(){if(this._active){this._isPaused=!0;let t,n;if(this.scopes){const s=this.scopes.slice();for(t=0,n=s.length;t<n;t++)s[t].pause()}for(t=0,n=this.effects.length;t<n;t++)this.effects[t].pause()}}resume(){if(this._active&&this._isPaused){this._isPaused=!1;let t,n;if(this.scopes){const r=this.scopes.slice();for(t=0,n=r.length;t<n;t++)r[t].resume()}const s=this.effects.slice();for(t=0,n=s.length;t<n;t++)s[t].resume()}}run(t){if(this._active){const n=pe;try{return pe=this,t()}finally{pe=n}}}on(){++this._on===1&&(this.prevScope=pe,pe=this)}off(){if(this._on>0&&--this._on===0){if(pe===this)pe=this.prevScope;else{let t=pe;for(;t;){if(t.prevScope===this){t.prevScope=this.prevScope;break}t=t.prevScope}}this.prevScope=void 0}}stop(t){if(this._active){this._active=!1;let n,s;for(n=0,s=this.effects.length;n<s;n++)this.effects[n].stop();for(this.effects.length=0,n=0,s=this.cleanups.length;n<s;n++)this.cleanups[n]();if(this.cleanups.length=0,this.scopes){const r=this.scopes.slice();for(n=0,s=r.length;n<s;n++)r[n].stop(!0);this.scopes.length=0}if(!this.detached&&this.parent&&!t){const r=this.parent.scopes.pop();r&&r!==this&&(this.parent.scopes[this.index]=r,r.index=this.index)}this.parent=void 0}}}function pi(){return pe}let te;const ns=new WeakSet;class nr{constructor(t){this.fn=t,this.deps=void 0,this.depsTail=void 0,this.flags=5,this.next=void 0,this.cleanup=void 0,this.scheduler=void 0,pe&&(pe.active?pe.effects.push(this):this.flags&=-2)}pause(){this.flags|=64}resume(){this.flags&64&&(this.flags&=-65,ns.has(this)&&(ns.delete(this),this.trigger()))}notify(){this.flags&2&&!(this.flags&32)||this.flags&8||rr(this)}run(){if(!(this.flags&1))return this.fn();this.flags|=2,cr(this),or(this);const t=te,n=Ve;te=this,Ve=!0;try{return this.fn()}finally{ir(this),te=t,Ve=n,this.flags&=-3}}stop(){if(this.flags&1){for(let t=this.deps;t;t=t.nextDep)is(t);this.deps=this.depsTail=void 0,cr(this),this.onStop&&this.onStop(),this.flags&=-2}}trigger(){this.flags&64?ns.add(this):this.scheduler?this.scheduler():this.runIfDirty()}runIfDirty(){os(this)&&this.run()}get dirty(){return os(this)}}let sr=0,Lt,Kt;function rr(e,t=!1){if(e.flags|=8,t){e.next=Kt,Kt=e;return}e.next=Lt,Lt=e}function ss(){sr++}function rs(){if(--sr>0)return;if(Kt){let t=Kt;for(Kt=void 0;t;){const n=t.next;t.next=void 0,t.flags&=-9,t=n}}let e;for(;Lt;){let t=Lt;for(Lt=void 0;t;){const n=t.next;if(t.next=void 0,t.flags&=-9,t.flags&1)try{t.trigger()}catch(s){e||(e=s)}t=n}}if(e)throw e}function or(e){for(let t=e.deps;t;t=t.nextDep)t.version=-1,t.prevActiveLink=t.dep.activeLink,t.dep.activeLink=t}function ir(e){let t,n=e.depsTail,s=n;for(;s;){const r=s.prevDep;s.version===-1?(s===n&&(n=r),is(s),hi(s)):t=s,s.dep.activeLink=s.prevActiveLink,s.prevActiveLink=void 0,s=r}e.deps=t,e.depsTail=n}function os(e){for(let t=e.deps;t;t=t.nextDep)if(t.dep.version!==t.version||t.dep.computed&&(lr(t.dep.computed)||t.dep.version!==t.version))return!0;return!!e._dirty}function lr(e){if(e.flags&4&&!(e.flags&16)||(e.flags&=-17,e.globalVersion===jt)||(e.globalVersion=jt,!e.isSSR&&e.flags&128&&(!e.deps&&!e._dirty||!os(e))))return;e.flags|=2;const t=e.dep,n=te,s=Ve;te=e,Ve=!0;try{or(e);const r=e.fn(e._value);(t.version===0||Ke(r,e._value))&&(e.flags|=128,e._value=r,t.version++)}catch(r){throw t.version++,r}finally{te=n,Ve=s,ir(e),e.flags&=-3}}function is(e,t=!1){const{dep:n,prevSub:s,nextSub:r}=e;if(s&&(s.nextSub=r,e.prevSub=void 0),r&&(r.prevSub=s,e.nextSub=void 0),n.subs===e&&(n.subs=s,!s&&n.computed)){n.computed.flags&=-5;for(let o=n.computed.deps;o;o=o.nextDep)is(o,!0)}!t&&!--n.sc&&n.map&&n.map.delete(n.key)}function hi(e){const{prevDep:t,nextDep:n}=e;t&&(t.nextDep=n,e.prevDep=void 0),n&&(n.prevDep=t,e.nextDep=void 0)}let Ve=!0;const ar=[];function je(){ar.push(Ve),Ve=!1}function He(){const e=ar.pop();Ve=e===void 0?!0:e}function cr(e){const{cleanup:t}=e;if(e.cleanup=void 0,t){const n=te;te=void 0;try{t()}finally{te=n}}}let jt=0;class gi{constructor(t,n){this.sub=t,this.dep=n,this.version=n.version,this.nextDep=this.prevDep=this.nextSub=this.prevSub=this.prevActiveLink=void 0}}class ls{constructor(t){this.computed=t,this.version=0,this.activeLink=void 0,this.subs=void 0,this.map=void 0,this.key=void 0,this.sc=0,this.__v_skip=!0}track(t){if(!te||!Ve||te===this.computed)return;let n=this.activeLink;if(n===void 0||n.sub!==te)n=this.activeLink=new gi(te,this),te.deps?(n.prevDep=te.depsTail,te.depsTail.nextDep=n,te.depsTail=n):te.deps=te.depsTail=n,ur(n);else if(n.version===-1&&(n.version=this.version,n.nextDep)){const s=n.nextDep;s.prevDep=n.prevDep,n.prevDep&&(n.prevDep.nextDep=s),n.prevDep=te.depsTail,n.nextDep=void 0,te.depsTail.nextDep=n,te.depsTail=n,te.deps===n&&(te.deps=s)}return n}trigger(t){this.version++,jt++,this.notify(t)}notify(t){ss();try{for(let n=this.subs;n;n=n.prevSub)n.sub.notify()&&n.sub.dep.notify()}finally{rs()}}}function ur(e){if(e.dep.sc++,e.sub.flags&4){const t=e.dep.computed;if(t&&!e.dep.subs){t.flags|=20;for(let s=t.deps;s;s=s.nextDep)ur(s)}const n=e.dep.subs;n!==e&&(e.prevSub=n,n&&(n.nextSub=e)),e.dep.subs=e}}const as=new WeakMap,ht=Symbol(""),cs=Symbol(""),Ht=Symbol("");function me(e,t,n){if(Ve&&te){let s=as.get(e);s||as.set(e,s=new Map);let r=s.get(n);r||(s.set(n,r=new ls),r.map=s,r.key=n),r.track()}}function Qe(e,t,n,s,r,o){const i=as.get(e);if(!i){jt++;return}const l=a=>{a&&a.trigger()};if(ss(),t==="clear")i.forEach(l);else{const a=P(e),d=a&&Zn(n);if(a&&n==="length"){const u=Number(s);i.forEach((p,x)=>{(x==="length"||x===Ht||!Pe(x)&&x>=u)&&l(p)})}else switch((n!==void 0||i.has(void 0))&&l(i.get(n)),d&&l(i.get(Ht)),t){case"add":a?d&&l(i.get("length")):(l(i.get(ht)),$t(e)&&l(i.get(cs)));break;case"delete":a||(l(i.get(ht)),$t(e)&&l(i.get(cs)));break;case"set":$t(e)&&l(i.get(ht));break}}rs()}function Et(e){const t=q(e);return t===e?t:(me(t,"iterate",Ht),Me(e)?t:t.map(Fe))}function vn(e){return me(e=q(e),"iterate",Ht),e}function Ue(e,t){return et(e)?At(gt(e)?Fe(t):t):Fe(t)}const mi={__proto__:null,[Symbol.iterator](){return us(this,Symbol.iterator,e=>Ue(this,e))},concat(...e){return Et(this).concat(...e.map(t=>P(t)?Et(t):t))},entries(){return us(this,"entries",e=>(e[1]=Ue(this,e[1]),e))},every(e,t){return Ze(this,"every",e,t,void 0,arguments)},filter(e,t){return Ze(this,"filter",e,t,n=>n.map(s=>Ue(this,s)),arguments)},find(e,t){return Ze(this,"find",e,t,n=>Ue(this,n),arguments)},findIndex(e,t){return Ze(this,"findIndex",e,t,void 0,arguments)},findLast(e,t){return Ze(this,"findLast",e,t,n=>Ue(this,n),arguments)},findLastIndex(e,t){return Ze(this,"findLastIndex",e,t,void 0,arguments)},forEach(e,t){return Ze(this,"forEach",e,t,void 0,arguments)},includes(...e){return fs(this,"includes",e)},indexOf(...e){return fs(this,"indexOf",e)},join(e){return Et(this).join(e)},lastIndexOf(...e){return fs(this,"lastIndexOf",e)},map(e,t){return Ze(this,"map",e,t,void 0,arguments)},pop(){return Ut(this,"pop")},push(...e){return Ut(this,"push",e)},reduce(e,...t){return fr(this,"reduce",e,t)},reduceRight(e,...t){return fr(this,"reduceRight",e,t)},shift(){return Ut(this,"shift")},some(e,t){return Ze(this,"some",e,t,void 0,arguments)},splice(...e){return Ut(this,"splice",e)},toReversed(){return Et(this).toReversed()},toSorted(e){return Et(this).toSorted(e)},toSpliced(...e){return Et(this).toSpliced(...e)},unshift(...e){return Ut(this,"unshift",e)},values(){return us(this,"values",e=>Ue(this,e))}};function us(e,t,n){const s=vn(e),r=s[t]();return s!==e&&!Me(e)&&(r._next=r.next,r.next=()=>{const o=r._next();return o.done||(o.value=n(o.value)),o}),r}const vi=Array.prototype;function Ze(e,t,n,s,r,o){const i=vn(e),l=i!==e&&!Me(e),a=i[t];if(a!==vi[t]){const p=a.apply(e,o);return l?Fe(p):p}let d=n;i!==e&&(l?d=function(p,x){return n.call(this,Ue(e,p),x,e)}:n.length>2&&(d=function(p,x){return n.call(this,p,x,e)}));const u=a.call(i,d,s);return l&&r?r(u):u}function fr(e,t,n,s){const r=vn(e),o=r!==e&&!Me(e);let i=n,l=!1;r!==e&&(o?(l=s.length===0,i=function(d,u,p){return l&&(l=!1,d=Ue(e,d)),n.call(this,d,Ue(e,u),p,e)}):n.length>3&&(i=function(d,u,p){return n.call(this,d,u,p,e)}));const a=r[t](i,...s);return l?Ue(e,a):a}function fs(e,t,n){const s=q(e);me(s,"iterate",Ht);const r=s[t](...n);return(r===-1||r===!1)&&hs(n[0])?(n[0]=q(n[0]),s[t](...n)):r}function Ut(e,t,n=[]){je(),ss();const s=q(e)[t].apply(e,n);return rs(),He(),s}const bi=Xn("__proto__,__v_isRef,__isVue"),dr=new Set(Object.getOwnPropertyNames(Symbol).filter(e=>e!=="arguments"&&e!=="caller").map(e=>Symbol[e]).filter(Pe));function yi(e){Pe(e)||(e=String(e));const t=q(this);return me(t,"has",e),t.hasOwnProperty(e)}class pr{constructor(t=!1,n=!1){this._isReadonly=t,this._isShallow=n}get(t,n,s){if(n==="__v_skip")return t.__v_skip;const r=this._isReadonly,o=this._isShallow;if(n==="__v_isReactive")return!r;if(n==="__v_isReadonly")return r;if(n==="__v_isShallow")return o;if(n==="__v_raw")return s===(r?o?yr:br:o?vr:mr).get(t)||Object.getPrototypeOf(t)===Object.getPrototypeOf(s)?t:void 0;const i=P(t);if(!r){let a;if(i&&(a=mi[n]))return a;if(n==="hasOwnProperty")return yi}const l=Reflect.get(t,n,he(t)?t:s);if((Pe(n)?dr.has(n):bi(n))||(r||me(t,"get",n),o))return l;if(he(l)){const a=i&&Zn(n)?l:l.value;return r&&Y(a)?ps(a):a}return Y(l)?r?ps(l):Bt(l):l}}class hr extends pr{constructor(t=!1){super(!1,t)}set(t,n,s,r){let o=t[n];const i=P(t)&&Zn(n);if(!this._isShallow){const d=et(o);if(!Me(s)&&!et(s)&&(o=q(o),s=q(s)),!i&&he(o)&&!he(s))return d||(o.value=s),!0}const l=i?Number(n)<t.length:J(t,n),a=Reflect.set(t,n,s,he(t)?t:r);return t===q(r)&&a&&(l?Ke(s,o)&&Qe(t,"set",n,s):Qe(t,"add",n,s)),a}deleteProperty(t,n){const s=J(t,n);t[n];const r=Reflect.deleteProperty(t,n);return r&&s&&Qe(t,"delete",n,void 0),r}has(t,n){const s=Reflect.has(t,n);return(!Pe(n)||!dr.has(n))&&me(t,"has",n),s}ownKeys(t){return me(t,"iterate",P(t)?"length":ht),Reflect.ownKeys(t)}}class gr extends pr{constructor(t=!1){super(!0,t)}set(t,n){return!0}deleteProperty(t,n){return!0}}const xi=new hr,_i=new gr,wi=new hr(!0),Si=new gr(!0),ds=e=>e,bn=e=>Reflect.getPrototypeOf(e);function ki(e,t,n){return function(...s){const r=this.__v_raw,o=q(r),i=$t(o),l=e==="entries"||e===Symbol.iterator&&i,a=e==="keys"&&i,d=r[e](...s),u=n?ds:t?At:Fe;return!t&&me(o,"iterate",a?cs:ht),de(Object.create(d),{next(){const{value:p,done:x}=d.next();return x?{value:p,done:x}:{value:l?[u(p[0]),u(p[1])]:u(p),done:x}}})}}function yn(e){return function(...t){return e==="delete"?!1:e==="clear"?void 0:this}}function Ci(e,t){const n={get(r){const o=this.__v_raw,i=q(o),l=q(r);e||(Ke(r,l)&&me(i,"get",r),me(i,"get",l));const{has:a}=bn(i),d=t?ds:e?At:Fe;if(a.call(i,r))return d(o.get(r));if(a.call(i,l))return d(o.get(l));o!==i&&o.get(r)},get size(){const r=this.__v_raw;return!e&&me(q(r),"iterate",ht),r.size},has(r){const o=this.__v_raw,i=q(o),l=q(r);return e||(Ke(r,l)&&me(i,"has",r),me(i,"has",l)),r===l?o.has(r):o.has(r)||o.has(l)},forEach(r,o){const i=this,l=i.__v_raw,a=q(l),d=t?ds:e?At:Fe;return!e&&me(a,"iterate",ht),l.forEach((u,p)=>r.call(o,d(u),d(p),i))}};return de(n,e?{add:yn("add"),set:yn("set"),delete:yn("delete"),clear:yn("clear")}:{add(r){const o=q(this),i=bn(o),l=q(r),a=!t&&!Me(r)&&!et(r)?l:r;return i.has.call(o,a)||Ke(r,a)&&i.has.call(o,r)||Ke(l,a)&&i.has.call(o,l)||(o.add(a),Qe(o,"add",a,a)),this},set(r,o){!t&&!Me(o)&&!et(o)&&(o=q(o));const i=q(this),{has:l,get:a}=bn(i);let d=l.call(i,r);d||(r=q(r),d=l.call(i,r));const u=a.call(i,r);return i.set(r,o),d?Ke(o,u)&&Qe(i,"set",r,o):Qe(i,"add",r,o),this},delete(r){const o=q(this),{has:i,get:l}=bn(o);let a=i.call(o,r);a||(r=q(r),a=i.call(o,r)),l&&l.call(o,r);const d=o.delete(r);return a&&Qe(o,"delete",r,void 0),d},clear(){const r=q(this),o=r.size!==0,i=r.clear();return o&&Qe(r,"clear",void 0,void 0),i}}),["keys","values","entries",Symbol.iterator].forEach(r=>{n[r]=ki(r,e,t)}),n}function xn(e,t){const n=Ci(e,t);return(s,r,o)=>r==="__v_isReactive"?!e:r==="__v_isReadonly"?e:r==="__v_raw"?s:Reflect.get(J(n,r)&&r in s?n:s,r,o)}const Ti={get:xn(!1,!1)},$i={get:xn(!1,!0)},Ei={get:xn(!0,!1)},Ai={get:xn(!0,!0)},mr=new WeakMap,vr=new WeakMap,br=new WeakMap,yr=new WeakMap;function Ri(e){switch(e){case"Object":case"Array":return 1;case"Map":case"Set":case"WeakMap":case"WeakSet":return 2;default:return 0}}function Bt(e){return et(e)?e:_n(e,!1,xi,Ti,mr)}function Ii(e){return _n(e,!1,wi,$i,vr)}function ps(e){return _n(e,!0,_i,Ei,br)}function Su(e){return _n(e,!0,Si,Ai,yr)}function _n(e,t,n,s,r){if(!Y(e)||e.__v_raw&&!(t&&e.__v_isReactive)||e.__v_skip||!Object.isExtensible(e))return e;const o=r.get(e);if(o)return o;const i=Ri(ni(e));if(i===0)return e;const l=new Proxy(e,i===2?s:n);return r.set(e,l),l}function gt(e){return et(e)?gt(e.__v_raw):!!(e&&e.__v_isReactive)}function et(e){return!!(e&&e.__v_isReadonly)}function Me(e){return!!(e&&e.__v_isShallow)}function hs(e){return e?!!e.__v_raw:!1}function q(e){const t=e&&e.__v_raw;return t?q(t):e}function Mi(e){return!J(e,"__v_skip")&&Object.isExtensible(e)&&Xs(e,"__v_skip",!0),e}const Fe=e=>Y(e)?Bt(e):e,At=e=>Y(e)?ps(e):e;function he(e){return e?e.__v_isRef===!0:!1}function B(e){return Oi(e,!1)}function Oi(e,t){return he(e)?e:new Pi(e,t)}class Pi{constructor(t,n){this.dep=new ls,this.__v_isRef=!0,this.__v_isShallow=!1,this._rawValue=n?t:q(t),this._value=n?t:Fe(t),this.__v_isShallow=n}get value(){return this.dep.track(),this._value}set value(t){const n=this._rawValue,s=this.__v_isShallow||Me(t)||et(t);t=s?t:q(t),Ke(t,n)&&(this._rawValue=t,this._value=s?t:Fe(t),this.dep.trigger())}}function Ce(e){return he(e)?e.value:e}const Ni={get:(e,t,n)=>t==="__v_raw"?e:Ce(Reflect.get(e,t,n)),set:(e,t,n,s)=>{const r=e[t];return he(r)&&!he(n)?(r.value=n,!0):Reflect.set(e,t,n,s)}};function xr(e){return gt(e)?e:new Proxy(e,Ni)}class Vi{constructor(t,n,s){this.fn=t,this.setter=n,this._value=void 0,this.dep=new ls(this),this.__v_isRef=!0,this.deps=void 0,this.depsTail=void 0,this.flags=16,this.globalVersion=jt-1,this.next=void 0,this.effect=this,this.__v_isReadonly=!n,this.isSSR=s}notify(){if(this.flags|=16,!(this.flags&8)&&te!==this)return rr(this,!0),!0}get value(){const t=this.dep.track();return lr(this),t&&(t.version=this.dep.version),this._value}set value(t){this.setter&&this.setter(t)}}function Fi(e,t,n=!1){let s,r;return F(e)?s=e:(s=e.get,r=e.set),new Vi(s,r,n)}const wn={},Sn=new WeakMap;let mt;function Di(e,t=!1,n=mt){if(n){let s=Sn.get(n);s||Sn.set(n,s=[]),s.push(e)}}function Li(e,t,n=Z){const{immediate:s,deep:r,once:o,scheduler:i,augmentJob:l,call:a}=n,d=R=>r?R:Me(R)||r===!1||r===0?tt(R,1):tt(R);let u,p,x,k,L=!1,A=!1;if(he(e)?(p=()=>e.value,L=Me(e)):gt(e)?(p=()=>d(e),L=!0):P(e)?(A=!0,L=e.some(R=>gt(R)||Me(R)),p=()=>e.map(R=>{if(he(R))return R.value;if(gt(R))return d(R);if(F(R))return a?a(R,2):R()})):F(e)?t?p=a?()=>a(e,2):e:p=()=>{if(x){je();try{x()}finally{He()}}const R=mt;mt=u;try{return a?a(e,3,[k]):e(k)}finally{mt=R}}:p=Le,t&&r){const R=p,ne=r===!0?1/0:r;p=()=>tt(R(),ne)}const z=pi(),W=()=>{u.stop(),z&&z.active&&Qn(z.effects,u)};if(o&&t){const R=t;t=(...ne)=>{const fe=R(...ne);return W(),fe}}let U=A?new Array(e.length).fill(wn):wn;const K=R=>{if(!(!(u.flags&1)||!u.dirty&&!R))if(t){const ne=u.run();if(R||r||L||(A?ne.some((fe,$e)=>Ke(fe,U[$e])):Ke(ne,U))){x&&x();const fe=mt;mt=u;try{const $e=[ne,U===wn?void 0:A&&U[0]===wn?[]:U,k];U=ne,a?a(t,3,$e):t(...$e)}finally{mt=fe}}}else u.run()};return l&&l(K),u=new nr(p),u.scheduler=i?()=>i(K,!1):K,k=R=>Di(R,!1,u),x=u.onStop=()=>{const R=Sn.get(u);if(R){if(a)a(R,4);else for(const ne of R)ne();Sn.delete(u)}},t?s?K(!0):U=u.run():i?i(K.bind(null,!0),!0):u.run(),W.pause=u.pause.bind(u),W.resume=u.resume.bind(u),W.stop=W,W}function tt(e,t=1/0,n){if(t<=0||!Y(e)||e.__v_skip||(n=n||new Map,(n.get(e)||0)>=t))return e;if(n.set(e,t),t--,he(e))tt(e.value,t,n);else if(P(e))for(let s=0;s<e.length;s++)tt(e[s],t,n);else if(fn(e)||$t(e))e.forEach(s=>{tt(s,t,n)});else if(Js(e)){for(const s in e)tt(e[s],t,n);for(const s of Object.getOwnPropertySymbols(e))Object.prototype.propertyIsEnumerable.call(e,s)&&tt(e[s],t,n)}return e}/**
* @vue/runtime-core v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/const zt=[];let gs=!1;function ku(e,...t){if(gs)return;gs=!0,je();const n=zt.length?zt[zt.length-1].component:null,s=n&&n.appContext.config.warnHandler,r=Ki();if(s)Rt(s,n,11,[e+t.map(o=>{var i,l;return(l=(i=o.toString)==null?void 0:i.call(o))!=null?l:JSON.stringify(o)}).join(""),n&&n.proxy,r.map(({vnode:o})=>`at <${go(n,o.type)}>`).join(`
`),r]);else{const o=[`[Vue warn]: ${e}`,...t];r.length&&o.push(`
`,...ji(r)),console.warn(...o)}He(),gs=!1}function Ki(){let e=zt[zt.length-1];if(!e)return[];const t=[];for(;e;){const n=t[0];n&&n.vnode===e?n.recurseCount++:t.push({vnode:e,recurseCount:0});const s=e.component&&e.component.parent;e=s&&s.vnode}return t}function ji(e){const t=[];return e.forEach((n,s)=>{t.push(...s===0?[]:[`
`],...Hi(n))}),t}function Hi({vnode:e,recurseCount:t}){const n=t>0?`... (${t} recursive calls)`:"",s=e.component?e.component.parent==null:!1,r=` at <${go(e.component,e.type,s)}`,o=">"+n;return e.props?[r,...Ui(e.props),o]:[r+o]}function Ui(e){const t=[],n=Object.keys(e);return n.slice(0,3).forEach(s=>{t.push(..._r(s,e[s]))}),n.length>3&&t.push(" ..."),t}function _r(e,t,n){return re(t)?(t=JSON.stringify(t),n?t:[`${e}=${t}`]):typeof t=="number"||typeof t=="boolean"||t==null?n?t:[`${e}=${t}`]:he(t)?(t=_r(e,q(t.value),!0),n?t:[`${e}=Ref<`,t,">"]):F(t)?[`${e}=fn${t.name?`<${t.name}>`:""}`]:(t=q(t),n?t:[`${e}=`,t])}function Rt(e,t,n,s){try{return s?e(...s):e()}catch(r){kn(r,t,n)}}function De(e,t,n,s){if(F(e)){const r=Rt(e,t,n,s);return r&&Gs(r)&&r.catch(o=>{kn(o,t,n)}),r}if(P(e)){const r=[];for(let o=0;o<e.length;o++)r.push(De(e[o],t,n,s));return r}}function kn(e,t,n,s=!0){const r=t?t.vnode:null,{errorHandler:o,throwUnhandledErrorInProduction:i}=t&&t.appContext.config||Z;if(t){let l=t.parent;const a=t.proxy,d=`https://vuejs.org/error-reference/#runtime-${n}`;for(;l;){const u=l.ec;if(u){for(let p=0;p<u.length;p++)if(u[p](e,a,d)===!1)return}l=l.parent}if(o){je(),Rt(o,null,10,[e,a,d]),He();return}}Bi(e,n,r,s,i)}function Bi(e,t,n,s=!0,r=!1){if(r)throw e;console.error(e)}const _e=[];let Be=-1;const It=[];let at=null,Mt=0;const wr=Promise.resolve();let Cn=null;function Tn(e){const t=Cn||wr;return e?t.then(this?e.bind(this):e):t}function zi(e){let t=Be+1,n=_e.length;for(;t<n;){const s=t+n>>>1,r=_e[s],o=Wt(r);o<e||o===e&&r.flags&2?t=s+1:n=s}return t}function ms(e){if(!(e.flags&1)){const t=Wt(e),n=_e[_e.length-1];!n||!(e.flags&2)&&t>=Wt(n)?_e.push(e):_e.splice(zi(t),0,e),e.flags|=1,Sr()}}function Sr(){Cn||(Cn=wr.then(Tr))}function Wi(e){if(!P(e))at&&e.id===-1?at.splice(Mt+1,0,e):e.flags&1||(It.push(e),e.flags|=1);else for(let t=0;t<e.length;t++)It.push(e[t]);Sr()}function kr(e,t,n=Be+1){for(;n<_e.length;n++){const s=_e[n];if(s&&s.flags&2){if(e&&s.id!==e.uid)continue;_e.splice(n,1),n--,s.flags&4&&(s.flags&=-2),s(),s.flags&4||(s.flags&=-2)}}}function Cr(e){if(It.length){const t=[...new Set(It)].sort((n,s)=>Wt(n)-Wt(s));if(It.length=0,at){for(let n=0;n<t.length;n++)at.push(t[n]);return}for(at=t,Mt=0;Mt<at.length;Mt++){const n=at[Mt];n.flags&4&&(n.flags&=-2),n.flags&8||n(),n.flags&=-2}at=null,Mt=0}}const Wt=e=>e.id==null?e.flags&2?-1:1/0:e.id;function Tr(e){try{for(Be=0;Be<_e.length;Be++){const t=_e[Be];t&&!(t.flags&8)&&(t.flags&4&&(t.flags&=-2),Rt(t,t.i,t.i?15:14),t.flags&4||(t.flags&=-2))}}finally{for(;Be<_e.length;Be++){const t=_e[Be];t&&(t.flags&=-2)}Be=-1,_e.length=0,Cr(),Cn=null,(_e.length||It.length)&&Tr()}}let ve=null,$r=null;function $n(e){const t=ve;return ve=e,$r=e&&e.type.__scopeId||null,t}function ie(e,t=ve,n){if(!t||e._n)return e;const s=(...r)=>{s._d&&Vn(-1);const o=$n(t),i=st.length;let l;try{l=e(...r)}finally{for(let a=st.length;a>i;a--)Rs();$n(o),s._d&&Vn(1)}return l};return s._n=!0,s._c=!0,s._d=!0,s}function ue(e,t){if(ve===null)return e;const n=Kn(ve),s=e.dirs||(e.dirs=[]);for(let r=0;r<t.length;r++){let[o,i,l,a=Z]=t[r];o&&(F(o)&&(o={mounted:o,updated:o}),o.deep&&tt(i),s.push({dir:o,instance:n,value:i,oldValue:void 0,arg:l,modifiers:a}))}return e}function vt(e,t,n,s){const r=e.dirs,o=t&&t.dirs;for(let i=0;i<r.length;i++){const l=r[i];o&&(l.oldValue=o[i].value);let a=l.dir[s];a&&(je(),De(a,n,8,[e.el,l,e,t]),He())}}function Gi(e,t){if(Se){let n=Se.provides;const s=Se.parent&&Se.parent.provides;s===n&&(n=Se.provides=Object.create(s)),n[e]=t}}function En(e,t,n=!1){const s=Ul();if(s||Pt){let r=Pt?Pt._context.provides:s?s.parent==null||s.ce?s.vnode.appContext&&s.vnode.appContext.provides:s.parent.provides:void 0;if(r&&e in r)return r[e];if(arguments.length>1)return n&&F(t)?t.call(s&&s.proxy):t}}const qi=Symbol.for("v-scx"),Ji=()=>En(qi);function bt(e,t,n){return Er(e,t,n)}function Er(e,t,n=Z){const{immediate:s,deep:r,flush:o,once:i}=n,l=de({},n),a=t&&s||!t&&o!=="post";let d;if(nn){if(o==="sync"){const k=Ji();d=k.__watcherHandles||(k.__watcherHandles=[])}else if(!a){const k=()=>{};return k.stop=Le,k.resume=Le,k.pause=Le,k}}const u=Se;l.call=(k,L,A)=>De(k,u,L,A);let p=!1;o==="post"?l.scheduler=k=>{Te(k,u&&u.suspense)}:o!=="sync"&&(p=!0,l.scheduler=(k,L)=>{L?k():ms(k)}),l.augmentJob=k=>{t&&(k.flags|=4),p&&(k.flags|=2,u&&(k.id=u.uid,k.i=u))};const x=Li(e,t,l);return nn&&(d?d.push(x):a&&x()),x}function Yi(e,t,n){const s=this.proxy,r=re(e)?e.includes(".")?Ar(s,e):()=>s[e]:e.bind(s,s);let o;F(t)?o=t:(o=t.handler,n=t);const i=tn(this),l=Er(r,o.bind(s),n);return i(),l}function Ar(e,t){const n=t.split(".");return()=>{let s=e;for(let r=0;r<n.length&&s;r++)s=s[n[r]];return s}}const Xi=Symbol("_vte"),An=e=>e.__isTeleport,vs=Symbol("_leaveCb");function Qi(e){let t=e[0];if(e.length>1){for(const n of e)if(n.type!==ze){t=n;break}}return t}function Rr(e){if(!ys(e))return An(e.type)&&e.children?Qi(e.children):e;if(e.component)return e.component.subTree;const{shapeFlag:t,children:n}=e;if(n){if(t&16)return n[0];if(t&32&&F(n.default))return n.default()}}function bs(e,t){if(e.shapeFlag&6&&e.component){e.transition=t;const n=e.component.subTree;bs(An(n.type)&&Rr(n)||n,t)}else e.shapeFlag&128?(e.ssContent.transition=t.clone(e.ssContent),e.ssFallback.transition=t.clone(e.ssFallback)):e.transition=t}function ct(e,t){return F(e)?de({name:e.name},t,{setup:e}):e}function Ir(e){e.ids=[e.ids[0]+e.ids[2]+++"-",0,0]}function Mr(e,t){let n;return!!((n=Object.getOwnPropertyDescriptor(e,t))&&!n.configurable)}const Rn=new WeakMap;function Gt(e,t,n,s,r=!1){if(P(e)){e.forEach((A,z)=>Gt(A,t&&(P(t)?t[z]:t),n,s,r));return}if(Ot(s)&&!r){s.shapeFlag&512&&s.type.__asyncResolved&&s.component.subTree.component&&Gt(e,t,n,s.component.subTree);return}const o=s.shapeFlag&4?Kn(s.component):s.el,i=r?null:o,{i:l,r:a}=e,d=t&&t.r,u=l.refs===Z?l.refs={}:l.refs,p=l.setupState,x=q(p),k=p===Z?zs:A=>Mr(u,A)?!1:J(x,A),L=(A,z)=>!(z&&Mr(u,z));if(d!=null&&d!==a){if(Or(t),re(d))u[d]=null,k(d)&&(p[d]=null);else if(he(d)){const A=t;L(d,A.k)&&(d.value=null),A.k&&(u[A.k]=null)}}if(F(a))Rt(a,l,12,[i,u]);else{const A=re(a),z=he(a);if(A||z){const W=()=>{if(e.f){const U=A?k(a)?p[a]:u[a]:L()||!e.k?a.value:u[e.k];if(r)P(U)&&Qn(U,o);else if(P(U))U.includes(o)||U.push(o);else if(A)u[a]=[o],k(a)&&(p[a]=u[a]);else{const K=[o];L(a,e.k)&&(a.value=K),e.k&&(u[e.k]=K)}}else A?(u[a]=i,k(a)&&(p[a]=i)):z&&(L(a,e.k)&&(a.value=i),e.k&&(u[e.k]=i))};if(i){const U=()=>{W(),Rn.delete(e)};U.id=-1,Rn.set(e,U),Te(U,n)}else Or(e),W()}}}function Or(e){const t=Rn.get(e);t&&(t.flags|=8,Rn.delete(e))}gn().requestIdleCallback,gn().cancelIdleCallback;const Ot=e=>!!e.type.__asyncLoader,ys=e=>e.type.__isKeepAlive;function Zi(e,t){Pr(e,"a",t)}function el(e,t){Pr(e,"da",t)}function Pr(e,t,n=Se){const s=e.__wdc||(e.__wdc=()=>{let r=n;for(;r;){if(r.isDeactivated)return;r=r.parent}return e()});if(In(t,s,n),n){let r=n.parent;for(;r&&r.parent;)ys(r.parent.vnode)&&tl(s,t,n,r),r=r.parent}}function tl(e,t,n,s){const r=In(t,e,s,!0);Mn(()=>{Qn(s[t],r)},n)}function In(e,t,n=Se,s=!1){if(n){const r=n[e]||(n[e]=[]),o=t.__weh||(t.__weh=(...i)=>{je();const l=tn(n),a=De(t,n,e,i);return l(),He(),a});return s?r.unshift(o):r.push(o),o}}const nt=e=>(t,n=Se)=>{(!nn||e==="sp")&&In(e,(...s)=>t(...s),n)},nl=nt("bm"),xs=nt("m"),sl=nt("bu"),rl=nt("u"),Nr=nt("bum"),Mn=nt("um"),ol=nt("sp"),il=nt("rtg"),ll=nt("rtc");function al(e,t=Se){In("ec",e,t)}const cl=Symbol.for("v-ndc");function ut(e,t,n,s){let r;const o=n,i=P(e);if(i||re(e)){const l=i&&gt(e);let a=!1,d=!1;l&&(a=!Me(e),d=et(e),e=vn(e)),r=new Array(e.length);for(let u=0,p=e.length;u<p;u++)r[u]=t(a?d?At(Fe(e[u])):Fe(e[u]):e[u],u,void 0,o)}else if(typeof e=="number"){r=new Array(e);for(let l=0;l<e;l++)r[l]=t(l+1,l,void 0,o)}else if(Y(e))if(e[Symbol.iterator])r=Array.from(e,(l,a)=>t(l,a,void 0,o));else{const l=Object.keys(e);r=new Array(l.length);for(let a=0,d=l.length;a<d;a++){const u=l[a];r[a]=t(e[u],u,a,o)}}else r=[];return r}function ul(e,t,n,s,r,o){if(n==null&&(n={}),ve.ce||ve.parent&&Ot(ve.parent)&&ve.parent.ce){const d=n,u=Object.keys(d).length>0;return t!=="default"&&(d.name=t),M(),xt(se,null,[X("slot",d,s&&s())],u?-2:64)}let i=e[t];i&&i._c&&(i._d=!1);const l=st.length;M();let a;try{const d=i&&Vr(i(n)),u=n.key||o||d&&d.key;a=xt(se,{key:(u&&!Pe(u)?u:`_${t}`)+(!d&&s?"_fb":"")},d||(s?s():[]),d&&e._===1?64:-2)}catch(d){for(let u=st.length;u>l;u--)Rs();throw d}finally{i&&i._c&&(i._d=!0)}return a.scopeId&&(a.slotScopeIds=[a.scopeId+"-s"]),a}function Vr(e){return e.some(t=>Xt(t)?!(t.type===ze||t.type===se&&!Vr(t.children)):!0)?e:null}const _s=e=>e?fo(e)?Kn(e):_s(e.parent):null,qt=de(Object.create(null),{$:e=>e,$el:e=>e.vnode.el,$data:e=>e.data,$props:e=>e.props,$attrs:e=>e.attrs,$slots:e=>e.slots,$refs:e=>e.refs,$parent:e=>_s(e.parent),$root:e=>_s(e.root),$host:e=>e.ce,$emit:e=>e.emit,$options:e=>Kr(e),$forceUpdate:e=>e.f||(e.f=()=>{ms(e.update)}),$nextTick:e=>e.n||(e.n=Tn.bind(e.proxy)),$watch:e=>Yi.bind(e)}),ws=(e,t)=>e!==Z&&!e.__isScriptSetup&&J(e,t),fl={get({_:e},t){if(t==="__v_skip")return!0;const{ctx:n,setupState:s,data:r,props:o,accessCache:i,type:l,appContext:a}=e;if(t[0]!=="$"){const x=i[t];if(x!==void 0)switch(x){case 1:return s[t];case 2:return r[t];case 4:return n[t];case 3:return o[t]}else{if(ws(s,t))return i[t]=1,s[t];if(r!==Z&&J(r,t))return i[t]=2,r[t];if(J(o,t))return i[t]=3,o[t];if(n!==Z&&J(n,t))return i[t]=4,n[t];Ss&&(i[t]=0)}}const d=qt[t];let u,p;if(d)return t==="$attrs"&&me(e.attrs,"get",""),d(e);if((u=l.__cssModules)&&(u=u[t]))return u;if(n!==Z&&J(n,t))return i[t]=4,n[t];if(p=a.config.globalProperties,J(p,t))return p[t]},set({_:e},t,n){const{data:s,setupState:r,ctx:o}=e;return ws(r,t)?(r[t]=n,!0):s!==Z&&J(s,t)?(s[t]=n,!0):J(e.props,t)||t[0]==="$"&&t.slice(1)in e?!1:(o[t]=n,!0)},has({_:{data:e,setupState:t,accessCache:n,ctx:s,appContext:r,props:o,type:i}},l){let a;return!!(n[l]||e!==Z&&l[0]!=="$"&&J(e,l)||ws(t,l)||J(o,l)||J(s,l)||J(qt,l)||J(r.config.globalProperties,l)||(a=i.__cssModules)&&a[l])},defineProperty(e,t,n){return n.get!=null?e._.accessCache[t]=0:J(n,"value")&&this.set(e,t,n.value,null),Reflect.defineProperty(e,t,n)}};function Fr(e){return P(e)?e.reduce((t,n)=>(t[n]=null,t),{}):e}let Ss=!0;function dl(e){const t=Kr(e),n=e.proxy,s=e.ctx;Ss=!1,t.beforeCreate&&Dr(t.beforeCreate,e,"bc");const{data:r,computed:o,methods:i,watch:l,provide:a,inject:d,created:u,beforeMount:p,mounted:x,beforeUpdate:k,updated:L,activated:A,deactivated:z,beforeDestroy:W,beforeUnmount:U,destroyed:K,unmounted:R,render:ne,renderTracked:fe,renderTriggered:$e,errorCaptured:Oe,serverPrefetch:ft,expose:ke,inheritAttrs:lt,components:oe,directives:E,filters:j}=t;if(d&&pl(d,s,null),i)for(const T in i){const _=i[T];F(_)&&(s[T]=_.bind(n))}if(r){const T=r.call(n,n);Y(T)&&(e.data=Bt(T))}if(Ss=!0,o)for(const T in o){const _=o[T],ae=F(_)?_.bind(n,n):F(_.get)?_.get.bind(n,n):Le,Ee=!F(_)&&F(_.set)?_.set.bind(n):Le,Ct=ye({get:ae,set:Ee});Object.defineProperty(s,T,{enumerable:!0,configurable:!0,get:()=>Ct.value,set:qe=>Ct.value=qe})}if(l)for(const T in l)Lr(l[T],s,n,T);if(a){const T=F(a)?a.call(n):a;Reflect.ownKeys(T).forEach(_=>{Gi(_,T[_])})}u&&Dr(u,e,"c");function N(T,_){P(_)?_.forEach(ae=>T(ae.bind(n))):_&&T(_.bind(n))}if(N(nl,p),N(xs,x),N(sl,k),N(rl,L),N(Zi,A),N(el,z),N(al,Oe),N(ll,fe),N(il,$e),N(Nr,U),N(Mn,R),N(ol,ft),P(ke))if(ke.length){const T=e.exposed||(e.exposed={});ke.forEach(_=>{Object.defineProperty(T,_,{get:()=>n[_],set:ae=>n[_]=ae,enumerable:!0})})}else e.exposed||(e.exposed={});ne&&e.render===Le&&(e.render=ne),lt!=null&&(e.inheritAttrs=lt),oe&&(e.components=oe),E&&(e.directives=E),ft&&Ir(e)}function pl(e,t,n=Le){P(e)&&(e=ks(e));for(const s in e){const r=e[s];let o;Y(r)?"default"in r?o=En(r.from||s,r.default,!0):o=En(r.from||s):o=En(r),he(o)?Object.defineProperty(t,s,{enumerable:!0,configurable:!0,get:()=>o.value,set:i=>o.value=i}):t[s]=o}}function Dr(e,t,n){De(P(e)?e.map(s=>s.bind(t.proxy)):e.bind(t.proxy),t,n)}function Lr(e,t,n,s){let r=s.includes(".")?Ar(n,s):()=>n[s];if(re(e)){const o=t[e];F(o)&&bt(r,o)}else if(F(e))bt(r,e.bind(n));else if(Y(e))if(P(e))e.forEach(o=>Lr(o,t,n,s));else{const o=F(e.handler)?e.handler.bind(n):t[e.handler];F(o)&&bt(r,o,e)}}function Kr(e){const t=e.type,{mixins:n,extends:s}=t,{mixins:r,optionsCache:o,config:{optionMergeStrategies:i}}=e.appContext,l=o.get(t);let a;return l?a=l:!r.length&&!n&&!s?a=t:(a={},r.length&&r.forEach(d=>On(a,d,i,!0)),On(a,t,i)),Y(t)&&o.set(t,a),a}function On(e,t,n,s=!1){const{mixins:r,extends:o}=t;o&&On(e,o,n,!0),r&&r.forEach(i=>On(e,i,n,!0));for(const i in t)if(!(s&&i==="expose")){const l=hl[i]||n&&n[i];e[i]=l?l(e[i],t[i]):t[i]}return e}const hl={data:jr,props:Hr,emits:Hr,methods:Jt,computed:Jt,beforeCreate:we,created:we,beforeMount:we,mounted:we,beforeUpdate:we,updated:we,beforeDestroy:we,beforeUnmount:we,destroyed:we,unmounted:we,activated:we,deactivated:we,errorCaptured:we,serverPrefetch:we,components:Jt,directives:Jt,watch:ml,provide:jr,inject:gl};function jr(e,t){return t?e?function(){return de(F(e)?e.call(this,this):e,F(t)?t.call(this,this):t)}:t:e}function gl(e,t){return Jt(ks(e),ks(t))}function ks(e){if(P(e)){const t={};for(let n=0;n<e.length;n++)t[e[n]]=e[n];return t}return e}function we(e,t){return e?[...new Set([].concat(e,t))]:t}function Jt(e,t){return e?de(Object.create(null),e,t):t}function Hr(e,t){return e?P(e)&&P(t)?[...new Set([...e,...t])]:de(Object.create(null),Fr(e),Fr(t??{})):t}function ml(e,t){if(!e)return t;if(!t)return e;const n=de(Object.create(null),e);for(const s in t)n[s]=we(e[s],t[s]);return n}function Ur(){return{app:null,config:{isNativeTag:zs,performance:!1,globalProperties:{},optionMergeStrategies:{},errorHandler:void 0,warnHandler:void 0,compilerOptions:{}},mixins:[],components:{},directives:{},provides:Object.create(null),optionsCache:new WeakMap,propsCache:new WeakMap,emitsCache:new WeakMap}}let vl=0;function bl(e,t){return function(s,r=null){F(s)||(s=de({},s)),r!=null&&!Y(r)&&(r=null);const o=Ur(),i=new WeakSet,l=[];let a=!1;const d=o.app={_uid:vl++,_component:s,_props:r,_container:null,_context:o,_instance:null,version:Ql,get config(){return o.config},set config(u){},use(u,...p){return i.has(u)||(u&&F(u.install)?(i.add(u),u.install(d,...p)):F(u)&&(i.add(u),u(d,...p))),d},mixin(u){return o.mixins.includes(u)||o.mixins.push(u),d},component(u,p){return p?(o.components[u]=p,d):o.components[u]},directive(u,p){return p?(o.directives[u]=p,d):o.directives[u]},mount(u,p,x){if(!a){const k=d._ceVNode||X(s,r);return k.appContext=o,x===!0?x="svg":x===!1&&(x=void 0),e(k,u,x),a=!0,d._container=u,u.__vue_app__=d,Kn(k.component)}},onUnmount(u){l.push(u)},unmount(){a&&(De(l,d._instance,16),e(null,d._container),delete d._container.__vue_app__)},provide(u,p){return o.provides[u]=p,d},runWithContext(u){const p=Pt;Pt=d;try{return u()}finally{Pt=p}}};return d}}let Pt=null;const yl=(e,t)=>t==="modelValue"||t==="model-value"?e.modelModifiers:e[`${t}Modifiers`]||e[`${Ne(t)}Modifiers`]||e[`${pt(t)}Modifiers`];function xl(e,t,...n){if(e.isUnmounted)return;const s=e.vnode.props||Z;let r=n;const o=t.startsWith("update:"),i=o&&yl(s,t.slice(7));i&&(i.trim&&(r=n.map(u=>re(u)?u.trim():u)),i.number&&(r=n.map(hn)));let l,a=s[l=es(t)]||s[l=es(Ne(t))];!a&&o&&(a=s[l=es(pt(t))]),a&&De(a,e,6,r);const d=s[l+"Once"];if(d){if(!e.emitted)e.emitted={};else if(e.emitted[l])return;e.emitted[l]=!0,De(d,e,6,r)}}const _l=new WeakMap;function Br(e,t,n=!1){const s=n?_l:t.emitsCache,r=s.get(e);if(r!==void 0)return r;const o=e.emits;let i={},l=!1;if(!F(e)){const a=d=>{const u=Br(d,t,!0);u&&(l=!0,de(i,u))};!n&&t.mixins.length&&t.mixins.forEach(a),e.extends&&a(e.extends),e.mixins&&e.mixins.forEach(a)}return!o&&!l?(Y(e)&&s.set(e,null),null):(P(o)?o.forEach(a=>i[a]=null):de(i,o),Y(e)&&s.set(e,i),i)}function Pn(e,t){return!e||!cn(t)?!1:(t=t.slice(2),t=t==="Once"?t:t.replace(/Once$/,""),J(e,t[0].toLowerCase()+t.slice(1))||J(e,pt(t))||J(e,t))}function Cu(){}function zr(e){const{type:t,vnode:n,proxy:s,withProxy:r,propsOptions:[o],slots:i,attrs:l,emit:a,render:d,renderCache:u,props:p,data:x,setupState:k,ctx:L,inheritAttrs:A}=e,z=$n(e);let W,U;try{if(n.shapeFlag&4){const R=r||s,ne=R;W=We(d.call(ne,R,u,p,k,x,L)),U=l}else{const R=t;W=We(R.length>1?R(p,{attrs:l,slots:i,emit:a}):R(p,null)),U=t.props?l:wl(l)}}catch(R){st.length=0,kn(R,e,1),W=X(ze)}let K=W;if(U&&A!==!1){const R=Object.keys(U),{shapeFlag:ne}=K;R.length&&ne&7&&(o&&R.some(un)&&(U=Sl(U,o)),K=Nt(K,U,!1,!0))}if(n.dirs&&(K=Nt(K,null,!1,!0),K.dirs=K.dirs?K.dirs.concat(n.dirs):n.dirs),n.transition){const R=An(K.type)&&Rr(K)||K;bs(R,n.transition)}return W=K,$n(z),W}const wl=e=>{let t;for(const n in e)(n==="class"||n==="style"||cn(n))&&((t||(t={}))[n]=e[n]);return t},Sl=(e,t)=>{const n={};for(const s in e)(!un(s)||!(s.slice(9)in t))&&(n[s]=e[s]);return n};function kl(e,t,n){const{props:s,children:r,component:o}=e,{props:i,children:l,patchFlag:a}=t,d=o.emitsOptions;if(t.dirs||t.transition)return!0;if(n&&a>=0){if(a&1024)return!0;if(a&16)return s?Wr(s,i,d):!!i;if(a&8){const u=t.dynamicProps;for(let p=0;p<u.length;p++){const x=u[p];if(Gr(i,s,x)&&!Pn(d,x))return!0}}}else return(r||l)&&(!l||!l.$stable)?!0:s===i?!1:s?i?Wr(s,i,d):!0:!!i;return!1}function Wr(e,t,n){const s=Object.keys(t);if(s.length!==Object.keys(e).length)return!0;for(let r=0;r<s.length;r++){const o=s[r];if(Gr(t,e,o)&&!Pn(n,o))return!0}return!1}function Gr(e,t,n){const s=e[n],r=t[n];return n==="style"&&Y(s)&&Y(r)?!Dt(s,r):s!==r}function Cl({vnode:e,parent:t,suspense:n},s){for(;t;){const r=t.subTree;if(r.suspense&&r.suspense.activeBranch===e&&(r.suspense.vnode.el=r.el=s,e=r),r===e)(e=t.vnode).el=s,t=t.parent;else break}n&&n.activeBranch===e&&(n.vnode.el=s)}const qr={},Jr=()=>Object.create(qr),Yr=e=>Object.getPrototypeOf(e)===qr;function Tl(e,t,n,s=!1){const r={},o=Jr();e.propsDefaults=Object.create(null),Xr(e,t,r,o);for(const i in e.propsOptions[0])i in r||(r[i]=void 0);n?e.props=s?r:Ii(r):e.type.props?e.props=r:e.props=o,e.attrs=o}function $l(e,t,n,s){const{props:r,attrs:o,vnode:{patchFlag:i}}=e,l=q(r),[a]=e.propsOptions;let d=!1;if((s||i>0)&&!(i&16)){if(i&8){const u=e.vnode.dynamicProps;for(let p=0;p<u.length;p++){let x=u[p];if(Pn(e.emitsOptions,x))continue;const k=t[x];if(a)if(J(o,x))k!==o[x]&&(o[x]=k,d=!0);else{const L=Ne(x);r[L]=Cs(a,l,L,k,e,!1)}else k!==o[x]&&(o[x]=k,d=!0)}}}else{Xr(e,t,r,o)&&(d=!0);let u;for(const p in l)(!t||!J(t,p)&&((u=pt(p))===p||!J(t,u)))&&(a?n&&(n[p]!==void 0||n[u]!==void 0)&&(r[p]=Cs(a,l,p,void 0,e,!0)):delete r[p]);if(o!==l)for(const p in o)(!t||!J(t,p))&&(delete o[p],d=!0)}d&&Qe(e.attrs,"set","")}function Xr(e,t,n,s){const[r,o]=e.propsOptions;let i=!1,l;if(t)for(let a in t){if(Ft(a))continue;const d=t[a];let u;r&&J(r,u=Ne(a))?!o||!o.includes(u)?n[u]=d:(l||(l={}))[u]=d:Pn(e.emitsOptions,a)||(!(a in s)||d!==s[a])&&(s[a]=d,i=!0)}if(o){const a=q(n),d=l||Z;for(let u=0;u<o.length;u++){const p=o[u];n[p]=Cs(r,a,p,d[p],e,!J(d,p))}}return i}function Cs(e,t,n,s,r,o){const i=e[n];if(i!=null){const l=J(i,"default");if(l&&s===void 0){const a=i.default;if(i.type!==Function&&!i.skipFactory&&F(a)){const{propsDefaults:d}=r;if(n in d)s=d[n];else{const u=tn(r);s=d[n]=a.call(null,t),u()}}else s=a;r.ce&&r.ce._setProp(n,s)}i[0]&&(o&&!l?s=!1:i[1]&&(s===""||s===pt(n))&&(s=!0))}return s}const El=new WeakMap;function Qr(e,t,n=!1){const s=n?El:t.propsCache,r=s.get(e);if(r)return r;const o=e.props,i={},l=[];let a=!1;if(!F(e)){const u=p=>{a=!0;const[x,k]=Qr(p,t,!0);de(i,x),k&&l.push(...k)};!n&&t.mixins.length&&t.mixins.forEach(u),e.extends&&u(e.extends),e.mixins&&e.mixins.forEach(u)}if(!o&&!a)return Y(e)&&s.set(e,Tt),Tt;if(P(o))for(let u=0;u<o.length;u++){const p=Ne(o[u]);Zr(p)&&(i[p]=Z)}else if(o)for(const u in o){const p=Ne(u);if(Zr(p)){const x=o[u],k=i[p]=P(x)||F(x)?{type:x}:de({},x),L=k.type;let A=!1,z=!0;if(P(L))for(let W=0;W<L.length;++W){const U=L[W],K=F(U)&&U.name;if(K==="Boolean"){A=!0;break}else K==="String"&&(z=!1)}else A=F(L)&&L.name==="Boolean";k[0]=A,k[1]=z,(A||J(k,"default"))&&l.push(p)}}const d=[i,l];return Y(e)&&s.set(e,d),d}function Zr(e){return e[0]!=="$"&&!Ft(e)}const Ts=e=>e==="_"||e==="_ctx"||e==="$stable",$s=e=>P(e)?e.map(We):[We(e)],Al=(e,t,n)=>{if(t._n)return t;const s=ie((...r)=>$s(t(...r)),n);return s._c=!1,s},eo=(e,t,n)=>{const s=e._ctx;for(const r in e){if(Ts(r))continue;const o=e[r];if(F(o))t[r]=Al(r,o,s);else if(o!=null){const i=$s(o);t[r]=()=>i}}},to=(e,t)=>{const n=$s(t);e.slots.default=()=>n},no=(e,t,n)=>{for(const s in t)(n||!Ts(s))&&(e[s]=t[s])},Rl=(e,t,n)=>{const s=e.slots=Jr();if(e.vnode.shapeFlag&32){const r=t._;r?(no(s,t,n),n&&Xs(s,"_",r,!0)):eo(t,s)}else t&&to(e,t)},Il=(e,t,n)=>{const{vnode:s,slots:r}=e;let o=!0,i=Z;if(s.shapeFlag&32){const l=t._;l?n&&l===1?o=!1:no(r,t,n):(o=!t.$stable,eo(t,r)),i=t}else t&&(to(e,t),i={default:1});if(o)for(const l in r)!Ts(l)&&i[l]==null&&delete r[l]},Te=Vl;function Ml(e){return Ol(e)}function Ol(e,t){const n=gn();n.__VUE__=!0;const{insert:s,remove:r,patchProp:o,createElement:i,createText:l,createComment:a,setText:d,setElementText:u,parentNode:p,nextSibling:x,setScopeId:k=Le,insertStaticContent:L}=e,A=(c,f,g,y=null,b=null,m=null,C=void 0,S=null,w=!!f.dynamicChildren)=>{if(c===f)return;c&&!Qt(c,f)&&(y=Yn(c),qe(c,b,m,!0),c=null),f.patchFlag===-2&&(w=!1,f.dynamicChildren=null);const{type:v,ref:O,shapeFlag:$}=f;switch(v){case Nn:z(c,f,g,y);break;case ze:W(c,f,g,y);break;case As:c==null&&U(f,g,y,C);break;case se:oe(c,f,g,y,b,m,C,S,w);break;default:$&1?ne(c,f,g,y,b,m,C,S,w):$&6?E(c,f,g,y,b,m,C,S,w):($&64||$&128)&&v.process(c,f,g,y,b,m,C,S,w,ln)}O!=null&&b?Gt(O,c&&c.ref,m,f||c,!f):O==null&&c&&c.ref!=null&&Gt(c.ref,null,m,c,!0)},z=(c,f,g,y)=>{if(c==null)s(f.el=l(f.children),g,y);else{const b=f.el=c.el;f.children!==c.children&&d(b,f.children)}},W=(c,f,g,y)=>{c==null?s(f.el=a(f.children||""),g,y):f.el=c.el},U=(c,f,g,y)=>{[c.el,c.anchor]=L(c.children,f,g,y,c.el,c.anchor)},K=({el:c,anchor:f},g,y)=>{let b;for(;c&&c!==f;)b=x(c),s(c,g,y),c=b;s(f,g,y)},R=({el:c,anchor:f})=>{let g;for(;c&&c!==f;)g=x(c),r(c),c=g;r(f)},ne=(c,f,g,y,b,m,C,S,w)=>{if(f.type==="svg"?C="svg":f.type==="math"&&(C="mathml"),c==null)fe(f,g,y,b,m,C,S,w);else{const v=c.el&&c.el._isVueCE?c.el:null;try{v&&v._beginPatch(),ft(c,f,b,m,C,S,w)}finally{v&&v._endPatch()}}},fe=(c,f,g,y,b,m,C,S)=>{let w,v;const{props:O,shapeFlag:$,transition:I,dirs:V}=c;if(w=c.el=i(c.type,m,O&&O.is,O),$&8?u(w,c.children):$&16&&Oe(c.children,w,null,y,b,Es(c,m),C,S),V&&vt(c,null,y,"created"),$e(w,c,c.scopeId,C,y),O){for(const ee in O)ee!=="value"&&!Ft(ee)&&o(w,ee,null,O[ee],m,y);"value"in O&&o(w,"value",null,O.value,m),(v=O.onVnodeBeforeMount)&&Ge(v,y,c)}V&&vt(c,null,y,"beforeMount");const G=Pl(b,I);G&&I.beforeEnter(w),s(w,f,g),((v=O&&O.onVnodeMounted)||G||V)&&Te(()=>{try{v&&Ge(v,y,c),G&&I.enter(w),V&&vt(c,null,y,"mounted")}finally{}},b)},$e=(c,f,g,y,b)=>{if(g&&k(c,g),y)for(let m=0;m<y.length;m++)k(c,y[m]);if(b){let m=b.subTree;if(f===m||lo(m.type)&&(m.ssContent===f||m.ssFallback===f)){const C=b.vnode;$e(c,C,C.scopeId,C.slotScopeIds,b.parent)}}},Oe=(c,f,g,y,b,m,C,S,w=0)=>{for(let v=w;v<c.length;v++){const O=c[v]=S?rt(c[v]):We(c[v]);A(null,O,f,g,y,b,m,C,S)}},ft=(c,f,g,y,b,m,C)=>{const S=f.el=c.el;let{patchFlag:w,dynamicChildren:v,dirs:O}=f;w|=c.patchFlag&16;const $=c.props||Z,I=f.props||Z;let V;if(g&&yt(g,!1),(V=I.onVnodeBeforeUpdate)&&Ge(V,g,f,c),O&&vt(f,c,g,"beforeUpdate"),g&&yt(g,!0),v&&(!c.dynamicChildren||c.dynamicChildren.length!==v.length)&&(w=0,C=!1,v=null),($.innerHTML&&I.innerHTML==null||$.textContent&&I.textContent==null)&&u(S,""),v?ke(c.dynamicChildren,v,S,g,y,Es(f,b),m):C||_(c,f,S,null,g,y,Es(f,b),m,!1),w>0){if(w&16)lt(S,$,I,g,b);else if(w&2&&$.class!==I.class&&o(S,"class",null,I.class,b),w&4&&o(S,"style",$.style,I.style,b),w&8){const G=f.dynamicProps;for(let ee=0;ee<G.length;ee++){const Q=G[ee],ce=$[Q],ge=I[Q];(ge!==ce||Q==="value")&&o(S,Q,ce,ge,b,g)}}w&1&&c.children!==f.children&&u(S,f.children)}else!C&&v==null&&lt(S,$,I,g,b);((V=I.onVnodeUpdated)||O)&&Te(()=>{V&&Ge(V,g,f,c),O&&vt(f,c,g,"updated")},y)},ke=(c,f,g,y,b,m,C)=>{for(let S=0;S<f.length;S++){const w=c[S],v=f[S],O=w.el&&(w.type===se||!Qt(w,v)||w.shapeFlag&198)?p(w.el):g;A(w,v,O,null,y,b,m,C,!0)}},lt=(c,f,g,y,b)=>{if(f!==g){if(f!==Z)for(const m in f)!Ft(m)&&!(m in g)&&o(c,m,f[m],null,b,y);for(const m in g){if(Ft(m))continue;const C=g[m],S=f[m];C!==S&&m!=="value"&&o(c,m,S,C,b,y)}"value"in g&&o(c,"value",f.value,g.value,b)}},oe=(c,f,g,y,b,m,C,S,w)=>{const v=f.el=c?c.el:l(""),O=f.anchor=c?c.anchor:l("");let{patchFlag:$,dynamicChildren:I,slotScopeIds:V}=f;V&&(S=S?S.concat(V):V),c==null?(s(v,g,y),s(O,g,y),Oe(f.children||[],g,O,b,m,C,S,w)):$>0&&$&64&&I&&c.dynamicChildren&&c.dynamicChildren.length===I.length?(ke(c.dynamicChildren,I,g,b,m,C,S),(f.key!=null||b&&f===b.subTree)&&so(c,f,!0)):_(c,f,g,O,b,m,C,S,w)},E=(c,f,g,y,b,m,C,S,w)=>{f.slotScopeIds=S,c==null?f.shapeFlag&512?b.ctx.activate(f,g,y,C,w):j(f,g,y,b,m,C,w):dt(c,f,w)},j=(c,f,g,y,b,m,C)=>{const S=c.component=Hl(c,y,b);if(ys(c)&&(S.ctx.renderer=ln),Bl(S,!1,C),S.asyncDep){if(b&&b.registerDep(S,N,C),!c.el){const w=S.subTree=X(ze);W(null,w,f,g),c.placeholder=w.el}}else N(S,c,f,g,b,m,C)},dt=(c,f,g)=>{const y=f.component=c.component;if(kl(c,f,g))if(y.asyncDep&&!y.asyncResolved){T(y,f,g);return}else y.next=f,y.update();else f.el=c.el,y.vnode=f},N=(c,f,g,y,b,m,C)=>{const S=()=>{if(c.isMounted){let{next:$,bu:I,u:V,parent:G,vnode:ee}=c;{const Ye=ro(c);if(Ye){$&&($.el=ee.el,T(c,$,C)),Ye.asyncDep.then(()=>{Te(()=>{c.isUnmounted||v()},b)});return}}let Q=$,ce;yt(c,!1),$?($.el=ee.el,T(c,$,C)):$=ee,I&&pn(I),(ce=$.props&&$.props.onVnodeBeforeUpdate)&&Ge(ce,G,$,ee),yt(c,!0);const ge=zr(c),Je=c.subTree;c.subTree=ge,A(Je,ge,p(Je.el),Yn(Je),c,b,m),$.el=ge.el,Q===null&&Cl(c,ge.el),V&&Te(V,b),(ce=$.props&&$.props.onVnodeUpdated)&&Te(()=>Ge(ce,G,$,ee),b)}else{let $;const{el:I,props:V}=f,{bm:G,m:ee,parent:Q,root:ce,type:ge}=c,Je=Ot(f);yt(c,!1),G&&pn(G),!Je&&($=V&&V.onVnodeBeforeMount)&&Ge($,Q,f),yt(c,!0);{ce.ce&&ce.ce._hasShadowRoot()&&ce.ce._injectChildStyle(ge,c.parent?c.parent.type:void 0);const Ye=c.subTree=zr(c);A(null,Ye,g,y,c,b,m),f.el=Ye.el}if(ee&&Te(ee,b),!Je&&($=V&&V.onVnodeMounted)){const Ye=f;Te(()=>Ge($,Q,Ye),b)}(f.shapeFlag&256||Q&&Ot(Q.vnode)&&Q.vnode.shapeFlag&256)&&c.a&&Te(c.a,b),c.isMounted=!0,f=g=y=null}};c.scope.on();const w=c.effect=new nr(S);c.scope.off();const v=c.update=w.run.bind(w),O=c.job=w.runIfDirty.bind(w);O.i=c,O.id=c.uid,w.scheduler=()=>ms(O),yt(c,!0),v()},T=(c,f,g)=>{f.component=c;const y=c.vnode.props;c.vnode=f,c.next=null,$l(c,f.props,y,g),Il(c,f.children,g),je(),kr(c),He()},_=(c,f,g,y,b,m,C,S,w=!1)=>{const v=c&&c.children,O=c?c.shapeFlag:0,$=f.children,{patchFlag:I,shapeFlag:V}=f;if(I>0){if(I&128){Ee(v,$,g,y,b,m,C,S,w);return}else if(I&256){ae(v,$,g,y,b,m,C,S,w);return}}V&8?(O&16&&on(v,b,m),$!==v&&u(g,$)):O&16?V&16?Ee(v,$,g,y,b,m,C,S,w):on(v,b,m,!0):(O&8&&u(g,""),V&16&&Oe($,g,y,b,m,C,S,w))},ae=(c,f,g,y,b,m,C,S,w)=>{c=c||Tt,f=f||Tt;const v=c.length,O=f.length,$=Math.min(v,O);let I;for(I=0;I<$;I++){const V=f[I]=w?rt(f[I]):We(f[I]);A(c[I],V,g,null,b,m,C,S,w)}v>O?on(c,b,m,!0,!1,$):Oe(f,g,y,b,m,C,S,w,$)},Ee=(c,f,g,y,b,m,C,S,w)=>{let v=0;const O=f.length;let $=c.length-1,I=O-1;for(;v<=$&&v<=I;){const V=c[v],G=f[v]=w?rt(f[v]):We(f[v]);if(Qt(V,G))A(V,G,g,null,b,m,C,S,w);else break;v++}for(;v<=$&&v<=I;){const V=c[$],G=f[I]=w?rt(f[I]):We(f[I]);if(Qt(V,G))A(V,G,g,null,b,m,C,S,w);else break;$--,I--}if(v>$){if(v<=I){const V=I+1,G=V<O?f[V].el:y;for(;v<=I;)A(null,f[v]=w?rt(f[v]):We(f[v]),g,G,b,m,C,S,w),v++}}else if(v>I)for(;v<=$;)qe(c[v],b,m,!0),v++;else{const V=v,G=v,ee=new Map;for(v=G;v<=I;v++){const Re=f[v]=w?rt(f[v]):We(f[v]);Re.key!=null&&ee.set(Re.key,v)}let Q,ce=0;const ge=I-G+1;let Je=!1,Ye=0;const an=new Array(ge);for(v=0;v<ge;v++)an[v]=0;for(v=V;v<=$;v++){const Re=c[v];if(ce>=ge){qe(Re,b,m,!0);continue}let Xe;if(Re.key!=null)Xe=ee.get(Re.key);else for(Q=G;Q<=I;Q++)if(an[Q-G]===0&&Qt(Re,f[Q])){Xe=Q;break}Xe===void 0?qe(Re,b,m,!0):(an[Xe-G]=v+1,Xe>=Ye?Ye=Xe:Je=!0,A(Re,f[Xe],g,null,b,m,C,S,w),ce++)}const Qo=Je?Nl(an):Tt;for(Q=Qo.length-1,v=ge-1;v>=0;v--){const Re=G+v,Xe=f[Re],Zo=f[Re+1],ei=Re+1<O?Zo.el||io(Zo):y;an[v]===0?A(null,Xe,g,ei,b,m,C,S,w):Je&&(Q<0||v!==Qo[Q]?Ct(Xe,g,ei,2):Q--)}}},Ct=(c,f,g,y,b=null)=>{const{el:m,type:C,transition:S,children:w,shapeFlag:v}=c;if(v&6){Ct(c.component.subTree,f,g,y);return}if(v&128){c.suspense.move(f,g,y);return}if(v&64){C.move(c,f,g,ln);return}if(C===se){s(m,f,g);for(let $=0;$<w.length;$++)Ct(w[$],f,g,y);s(c.anchor,f,g);return}if(C===As){K(c,f,g);return}if(y!==2&&v&1&&S)if(y===0)S.persisted&&!m[vs]?s(m,f,g):(S.beforeEnter(m),s(m,f,g),Te(()=>S.enter(m),b));else{const{leave:$,delayLeave:I,afterLeave:V}=S,G=()=>{c.ctx.isUnmounted?r(m):s(m,f,g)},ee=()=>{const Q=m._isLeaving||!!m[vs];m._isLeaving&&m[vs](!0),S.persisted&&!Q?G():$(m,()=>{G(),V&&V()})};I?I(m,G,ee):ee()}else s(m,f,g)},qe=(c,f,g,y=!1,b=!1)=>{const{type:m,props:C,ref:S,children:w,dynamicChildren:v,shapeFlag:O,patchFlag:$,dirs:I,cacheIndex:V,memo:G}=c;if($===-2&&(b=!1),S!=null&&(je(),Gt(S,null,g,c,!0),He()),V!=null&&(f.renderCache[V]=void 0),O&256){f.ctx.deactivate(c);return}const ee=O&1&&I,Q=!Ot(c);let ce;if(Q&&(ce=C&&C.onVnodeBeforeUnmount)&&Ge(ce,f,c),O&6)_u(c.component,g,y);else{if(O&128){c.suspense.unmount(g,y);return}ee&&vt(c,null,f,"beforeUnmount"),O&64?c.type.remove(c,f,g,ln,y):v&&!v.hasOnce&&(m!==se||$>0&&$&64)?on(v,f,g,!1,!0):(m===se&&$&384||!b&&O&16)&&on(w,f,g),y&&Yo(c)}const ge=G!=null&&V==null;(Q&&(ce=C&&C.onVnodeUnmounted)||ee||ge)&&Te(()=>{ce&&Ge(ce,f,c),ee&&vt(c,null,f,"unmounted"),ge&&(c.el=null)},g)},Yo=c=>{const{type:f,el:g,anchor:y,transition:b}=c;if(f===se){xu(g,y);return}if(f===As){R(c);return}const m=()=>{r(g),b&&!b.persisted&&b.afterLeave&&b.afterLeave()};if(c.shapeFlag&1&&b&&!b.persisted){const{leave:C,delayLeave:S}=b,w=()=>C(g,m);S?S(c.el,m,w):w()}else m()},xu=(c,f)=>{let g;for(;c!==f;)g=x(c),r(c),c=g;r(f)},_u=(c,f,g)=>{const{bum:y,scope:b,job:m,subTree:C,um:S,m:w,a:v}=c;oo(w),oo(v),y&&pn(y),b.stop(),m&&(m.flags|=8,qe(C,c,f,g)),S&&Te(S,f),Te(()=>{c.isUnmounted=!0},f)},on=(c,f,g,y=!1,b=!1,m=0)=>{for(let C=m;C<c.length;C++)qe(c[C],f,g,y,b)},Yn=c=>{if(c.shapeFlag&6)return Yn(c.component.subTree);if(c.shapeFlag&128)return c.suspense.next();const f=x(c.anchor||c.el),g=f&&f[Xi];return g?x(g):f};let Bs=!1;const Xo=(c,f,g)=>{let y;c==null?f._vnode&&(qe(f._vnode,null,null,!0),y=f._vnode.component):A(f._vnode||null,c,f,null,null,null,g),f._vnode=c,Bs||(Bs=!0,kr(y),Cr(),Bs=!1)},ln={p:A,um:qe,m:Ct,r:Yo,mt:j,mc:Oe,pc:_,pbc:ke,n:Yn,o:e};return{render:Xo,hydrate:void 0,createApp:bl(Xo)}}function Es({type:e,props:t},n){return n==="svg"&&e==="foreignObject"||n==="mathml"&&e==="annotation-xml"&&t&&t.encoding&&t.encoding.includes("html")?void 0:n}function yt({effect:e,job:t},n){n?(e.flags|=32,t.flags|=4):(e.flags&=-33,t.flags&=-5)}function Pl(e,t){return(!e||e&&!e.pendingBranch)&&t&&!t.persisted}function so(e,t,n=!1){const s=e.children,r=t.children;if(P(s)&&P(r))for(let o=0;o<s.length;o++){const i=s[o];let l=r[o];l.shapeFlag&1&&!l.dynamicChildren&&((l.patchFlag<=0||l.patchFlag===32)&&(l=r[o]=rt(r[o]),l.el=i.el),!n&&l.patchFlag!==-2&&so(i,l)),l.type===Nn&&(l.patchFlag===-1&&(l=r[o]=rt(l)),l.el=i.el),l.type===ze&&!l.el&&(l.el=i.el)}}function Nl(e){const t=e.slice(),n=[0];let s,r,o,i,l;const a=e.length;for(s=0;s<a;s++){const d=e[s];if(d!==0){if(r=n[n.length-1],e[r]<d){t[s]=r,n.push(s);continue}for(o=0,i=n.length-1;o<i;)l=o+i>>1,e[n[l]]<d?o=l+1:i=l;d<e[n[o]]&&(o>0&&(t[s]=n[o-1]),n[o]=s)}}for(o=n.length,i=n[o-1];o-- >0;)n[o]=i,i=t[i];return n}function ro(e){const t=e.subTree.component;if(t)return t.asyncDep&&!t.asyncResolved?t:ro(t)}function oo(e){if(e)for(let t=0;t<e.length;t++)e[t].flags|=8}function io(e){if(e.placeholder)return e.placeholder;const t=e.component;return t?io(t.subTree):null}const lo=e=>e.__isSuspense;function Vl(e,t){t&&t.pendingBranch?P(e)?t.effects.push(...e):t.effects.push(e):Wi(e)}const se=Symbol.for("v-fgt"),Nn=Symbol.for("v-txt"),ze=Symbol.for("v-cmt"),As=Symbol.for("v-stc"),st=[];let Ae=null;function M(e=!1){st.push(Ae=e?null:[])}function Rs(){st.pop(),Ae=st[st.length-1]||null}let Yt=1;function Vn(e,t=!1){Yt+=e,e<0&&Ae&&t&&(Ae.hasOnce=!0)}function ao(e){return e.dynamicChildren=Yt>0?Ae||Tt:null,Rs(),Yt>0&&Ae&&Ae.push(e),e}function D(e,t,n,s,r,o){return ao(h(e,t,n,s,r,o,!0))}function xt(e,t,n,s,r){return ao(X(e,t,n,s,r,!0))}function Xt(e){return e?e.__v_isVNode===!0:!1}function Qt(e,t){return e.type===t.type&&e.key===t.key}const co=({key:e})=>e??null,Fn=({ref:e,ref_key:t,ref_for:n})=>(typeof e=="number"&&(e=""+e),e!=null?re(e)||he(e)||F(e)?{i:ve,r:e,k:t,f:!!n}:e:null);function h(e,t=null,n=null,s=0,r=null,o=e===se?0:1,i=!1,l=!1){const a={__v_isVNode:!0,__v_skip:!0,type:e,props:t,key:t&&co(t),ref:t&&Fn(t),scopeId:$r,slotScopeIds:null,children:n,component:null,suspense:null,ssContent:null,ssFallback:null,dirs:null,transition:null,el:null,anchor:null,target:null,targetStart:null,targetAnchor:null,staticCount:0,shapeFlag:o,patchFlag:s,dynamicProps:r,dynamicChildren:null,appContext:null,ctx:ve};return l?(Dn(a,n),o&128&&e.normalize(a)):n&&(a.shapeFlag|=re(n)?8:16),Yt>0&&!i&&Ae&&(a.patchFlag>0||o&6)&&a.patchFlag!==32&&Ae.push(a),a}const X=Fl;function Fl(e,t=null,n=null,s=0,r=null,o=!1){if((!e||e===cl)&&(e=ze),Xt(e)){const l=Nt(e,t,!0);return n&&Dn(l,n),Yt>0&&!o&&Ae&&(l.shapeFlag&6?Ae[Ae.indexOf(e)]=l:Ae.push(l)),l.patchFlag=-2,l}if(Xl(e)&&(e=e.__vccOpts),t){t=Dl(t);let{class:l,style:a}=t;l&&!re(l)&&(t.class=Ie(l)),Y(a)&&(hs(a)&&!P(a)&&(a=de({},a)),t.style=mn(a))}const i=re(e)?1:lo(e)?128:An(e)?64:Y(e)?4:F(e)?2:0;return h(e,t,n,s,r,i,o,!0)}function Dl(e){return e?hs(e)||Yr(e)?de({},e):e:null}function Nt(e,t,n=!1,s=!1){const{props:r,ref:o,patchFlag:i,children:l,transition:a}=e,d=t?Ll(r||{},t):r,u={__v_isVNode:!0,__v_skip:!0,type:e.type,props:d,key:d&&co(d),ref:t&&t.ref?n&&o?P(o)?o.concat(Fn(t)):[o,Fn(t)]:Fn(t):o,scopeId:e.scopeId,slotScopeIds:e.slotScopeIds,children:l,target:e.target,targetStart:e.targetStart,targetAnchor:e.targetAnchor,staticCount:e.staticCount,shapeFlag:e.shapeFlag,patchFlag:t&&e.type!==se?i===-1?16:i|16:i,dynamicProps:e.dynamicProps,dynamicChildren:e.dynamicChildren,appContext:e.appContext,dirs:e.dirs,transition:a,component:e.component,suspense:e.suspense,ssContent:e.ssContent&&Nt(e.ssContent),ssFallback:e.ssFallback&&Nt(e.ssFallback),placeholder:e.placeholder,el:e.el,anchor:e.anchor,ctx:e.ctx,ce:e.ce};return a&&s&&bs(u,a.clone(u)),u}function Zt(e=" ",t=0){return X(Nn,null,e,t)}function be(e="",t=!1){return t?(M(),xt(ze,null,e)):X(ze,null,e)}function We(e){return e==null||typeof e=="boolean"?X(ze):P(e)?X(se,null,e.slice()):Xt(e)?rt(e):X(Nn,null,String(e))}function rt(e){return e.el===null&&e.patchFlag!==-1||e.memo?e:Nt(e)}function Dn(e,t){let n=0;const{shapeFlag:s}=e;if(t==null)t=null;else if(P(t))n=16;else if(typeof t=="object")if(s&65){const r=t.default;r&&(r._c&&(r._d=!1),Dn(e,r()),r._c&&(r._d=!0));return}else{n=32;const r=t._;!r&&!Yr(t)?t._ctx=ve:r===3&&ve&&(ve.slots._===1?t._=1:(t._=2,e.patchFlag|=1024))}else if(F(t)){if(s&65){Dn(e,{default:t});return}t={default:t,_ctx:ve},n=32}else t=String(t),s&64?(n=16,t=[Zt(t)]):n=8;e.children=t,e.shapeFlag|=n}function Ll(...e){const t={};for(let n=0;n<e.length;n++){const s=e[n];for(const r in s)if(r==="class")t.class!==s.class&&(t.class=Ie([t.class,s.class]));else if(r==="style")t.style=mn([t.style,s.style]);else if(cn(r)){const o=t[r],i=s[r];i&&o!==i&&!(P(o)&&o.includes(i))?t[r]=o?[].concat(o,i):i:i==null&&o==null&&!un(r)&&(t[r]=i)}else r!==""&&(t[r]=s[r])}return t}function Ge(e,t,n,s=null){De(e,t,7,[n,s])}const Kl=Ur();let jl=0;function Hl(e,t,n){const s=e.type,r=(t?t.appContext:e.appContext)||Kl,o={uid:jl++,vnode:e,type:s,parent:t,appContext:r,root:null,next:null,subTree:null,effect:null,update:null,job:null,scope:new di(!0),render:null,proxy:null,exposed:null,exposeProxy:null,withProxy:null,provides:t?t.provides:Object.create(r.provides),ids:t?t.ids:["",0,0],accessCache:null,renderCache:[],components:null,directives:null,propsOptions:Qr(s,r),emitsOptions:Br(s,r),emit:null,emitted:null,propsDefaults:Z,inheritAttrs:s.inheritAttrs,ctx:Z,data:Z,props:Z,attrs:Z,slots:Z,refs:Z,setupState:Z,setupContext:null,suspense:n,suspenseId:n?n.pendingId:0,asyncDep:null,asyncResolved:!1,isMounted:!1,isUnmounted:!1,isDeactivated:!1,bc:null,c:null,bm:null,m:null,bu:null,u:null,um:null,bum:null,da:null,a:null,rtg:null,rtc:null,ec:null,sp:null};return o.ctx={_:o},o.root=t?t.root:o,o.emit=xl.bind(null,o),e.ce&&e.ce(o),o}let Se=null;const Ul=()=>Se||ve;let Ln,en;{const e=gn(),t=(n,s)=>{let r;return(r=e[n])||(r=e[n]=[]),r.push(s),o=>{r.length>1?r.forEach(i=>i(o)):r[0](o)}};Ln=t("__VUE_INSTANCE_SETTERS__",n=>Se=n),en=t("__VUE_SSR_SETTERS__",n=>nn=n)}const tn=e=>{const t=Se;return Ln(e),e.scope.on(),()=>{e.scope.off(),Ln(t)}},uo=()=>{Se&&Se.scope.off(),Ln(null)};function fo(e){return e.vnode.shapeFlag&4}let nn=!1;function Bl(e,t=!1,n=!1){t&&en(t);const{props:s,children:r}=e.vnode,o=fo(e);Tl(e,s,o,t),Rl(e,r,n||t);const i=o?zl(e,t):void 0;return t&&en(!1),i}function zl(e,t){const n=e.type;e.accessCache=Object.create(null),e.proxy=new Proxy(e.ctx,fl);const{setup:s}=n;if(s){je();const r=e.setupContext=s.length>1?Gl(e):null,o=tn(e),i=Rt(s,e,0,[e.props,r]),l=Gs(i);if(He(),o(),(l||e.sp)&&!Ot(e)&&Ir(e),l){if(i.then(uo,uo),t)return i.then(a=>{en(!0);try{po(e,a,t)}finally{en(!1)}}).catch(a=>{kn(a,e,0)});e.asyncDep=i}else po(e,i)}else ho(e)}function po(e,t,n){F(t)?e.type.__ssrInlineRender?e.ssrRender=t:e.render=t:Y(t)&&(e.setupState=xr(t)),ho(e)}function ho(e,t,n){const s=e.type;e.render||(e.render=s.render||Le);{const r=tn(e);je();try{dl(e)}finally{He(),r()}}}const Wl={get(e,t){return me(e,"get",""),e[t]}};function Gl(e){const t=n=>{e.exposed=n||{}};return{attrs:new Proxy(e.attrs,Wl),slots:e.slots,emit:e.emit,expose:t}}function Kn(e){return e.exposed?e.exposeProxy||(e.exposeProxy=new Proxy(xr(Mi(e.exposed)),{get(t,n){if(n in t)return t[n];if(n in qt)return qt[n](e)},has(t,n){return n in t||n in qt}})):e.proxy}const ql=/(?:^|[-_])\w/g,Jl=e=>e.replace(ql,t=>t.toUpperCase()).replace(/[-_]/g,"");function Yl(e,t=!0){return F(e)?e.displayName||e.name:e.name||t&&e.__name}function go(e,t,n=!1){let s=Yl(t);if(!s&&t.__file){const r=t.__file.match(/([^/\\]+)\.\w+$/);r&&(s=r[1])}if(!s&&e){const r=o=>{for(const i in o)if(o[i]===t)return i};s=r(e.components)||e.parent&&r(e.parent.type.components)||r(e.appContext.components)}return s?Jl(s):n?"App":"Anonymous"}function Xl(e){return F(e)&&"__vccOpts"in e}const ye=(e,t)=>Fi(e,t,nn);function jn(e,t,n){try{Vn(-1);const s=arguments.length;return s===2?Y(t)&&!P(t)?Xt(t)?X(e,null,[t]):X(e,t):X(e,null,t):(s>3?n=Array.prototype.slice.call(arguments,2):s===3&&Xt(n)&&(n=[n]),X(e,t,n))}finally{Vn(1)}}const Ql="3.5.41";/**
* @vue/runtime-dom v3.5.41
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/let Is;const mo=typeof window<"u"&&window.trustedTypes;if(mo)try{Is=mo.createPolicy("vue",{createHTML:e=>e})}catch{}const vo=Is?e=>Is.createHTML(e):e=>e,Zl="http://www.w3.org/2000/svg",ea="http://www.w3.org/1998/Math/MathML",ot=typeof document<"u"?document:null,bo=ot&&ot.createElement("template"),ta={insert:(e,t,n)=>{t.insertBefore(e,n||null)},remove:e=>{const t=e.parentNode;t&&t.removeChild(e)},createElement:(e,t,n,s)=>{const r=t==="svg"?ot.createElementNS(Zl,e):t==="mathml"?ot.createElementNS(ea,e):n?ot.createElement(e,{is:n}):ot.createElement(e);return e==="select"&&s&&s.multiple!=null&&r.setAttribute("multiple",s.multiple),r},createText:e=>ot.createTextNode(e),createComment:e=>ot.createComment(e),setText:(e,t)=>{e.nodeValue=t},setElementText:(e,t)=>{e.textContent=t},parentNode:e=>e.parentNode,nextSibling:e=>e.nextSibling,querySelector:e=>ot.querySelector(e),setScopeId(e,t){e.setAttribute(t,"")},insertStaticContent(e,t,n,s,r,o){const i=n?n.previousSibling:t.lastChild;if(r&&(r===o||r.nextSibling))for(;t.insertBefore(r.cloneNode(!0),n),!(r===o||!(r=r.nextSibling)););else{bo.innerHTML=vo(s==="svg"?`<svg>${e}</svg>`:s==="mathml"?`<math>${e}</math>`:e);const l=bo.content;if(s==="svg"||s==="mathml"){const a=l.firstChild;for(;a.firstChild;)l.appendChild(a.firstChild);l.removeChild(a)}t.insertBefore(l,n)}return[i?i.nextSibling:t.firstChild,n?n.previousSibling:t.lastChild]}},na=Symbol("_vtc");function sa(e,t,n){const s=e[na];s&&(t=(t?[t,...s]:[...s]).join(" ")),t==null?e.removeAttribute("class"):n?e.setAttribute("class",t):e.className=t}const yo=Symbol("_vod"),ra=Symbol("_vsh"),oa=Symbol(""),ia=/(?:^|;)\s*display\s*:/;function la(e,t,n){const s=e.style,r=re(n);let o=!1;if(n&&!r){if(t)if(re(t))for(const i of t.split(";")){const l=i.slice(0,i.indexOf(":")).trim();n[l]==null&&sn(s,l,"")}else for(const i in t)n[i]==null&&sn(s,i,"");for(const i in n){i==="display"&&(o=!0);const l=n[i];l!=null?ca(e,i,!re(t)&&t?t[i]:void 0,l)||sn(s,i,l):sn(s,i,"")}}else if(r){if(t!==n){const i=s[oa];i&&(n+=";"+i),s.cssText=n,o=ia.test(n)}}else t&&e.removeAttribute("style");yo in e&&(e[yo]=o?s.display:"",e[ra]&&(s.display="none"))}const xo=/\s*!important$/;function sn(e,t,n){if(P(n))n.forEach(s=>sn(e,t,s));else if(n==null&&(n=""),t.startsWith("--"))e.setProperty(t,n);else{const s=aa(e,t);xo.test(n)?e.setProperty(pt(s),n.replace(xo,""),"important"):e[s]=n}}const _o=["Webkit","Moz","ms"],Ms={};function aa(e,t){const n=Ms[t];if(n)return n;let s=Ne(t);if(s!=="filter"&&s in e)return Ms[t]=s;s=Ys(s);for(let r=0;r<_o.length;r++){const o=_o[r]+s;if(o in e)return Ms[t]=o}return t}function ca(e,t,n,s){return e.tagName==="TEXTAREA"&&(t==="width"||t==="height")&&re(s)&&n===s}const wo="http://www.w3.org/1999/xlink";function So(e,t,n,s,r,o=ci(t)){s&&t.startsWith("xlink:")?n==null?e.removeAttributeNS(wo,t.slice(6,t.length)):e.setAttributeNS(wo,t,n):n==null||o&&!Zs(n)?e.removeAttribute(t):e.setAttribute(t,o?"":Pe(n)?String(n):n)}function ko(e,t,n,s,r){if(t==="innerHTML"||t==="textContent"){n!=null&&(e[t]=t==="innerHTML"?vo(n):n);return}const o=e.tagName;if(t==="value"&&o!=="PROGRESS"&&!o.includes("-")){const l=o==="OPTION"?e.getAttribute("value")||"":e.value,a=n==null?e.type==="checkbox"?"on":"":String(n);(l!==a||!("_value"in e))&&(e.value=a),n==null&&e.removeAttribute(t),e._value=n;return}let i=!1;if(n===""||n==null){const l=typeof e[t];l==="boolean"?n=Zs(n):n==null&&l==="string"?(n="",i=!0):l==="number"&&(n=0,i=!0)}try{e[t]=n}catch{}i&&e.removeAttribute(r||t)}function _t(e,t,n,s){e.addEventListener(t,n,s)}function ua(e,t,n,s){e.removeEventListener(t,n,s)}const Co=Symbol("_vei");function fa(e,t,n,s,r=null){const o=e[Co]||(e[Co]={}),i=o[t];if(s&&i)i.value=s;else{const[l,a]=ha(t);if(s){const d=o[t]=va(s,r);_t(e,l,d,a)}else i&&(ua(e,l,i,a),o[t]=void 0)}}const da=/(Once|Passive|Capture)$/,pa=/^on:?(?:Once|Passive|Capture)$/;function ha(e){let t,n;for(;(n=e.match(da))&&!pa.test(e);)t||(t={}),e=e.slice(0,e.length-n[1].length),t[n[1].toLowerCase()]=!0;return[e[2]===":"?e.slice(3):pt(e.slice(2)),t]}let Os=0;const ga=Promise.resolve(),ma=()=>Os||(ga.then(()=>Os=0),Os=Date.now());function va(e,t){const n=s=>{if(!s._vts)s._vts=Date.now();else if(s._vts<=n.attached)return;const r=n.value;if(P(r)){const o=s.stopImmediatePropagation;s.stopImmediatePropagation=()=>{o.call(s),s._stopped=!0};const i=r.slice(),l=[s];for(let a=0;a<i.length&&!s._stopped;a++){const d=i[a];d&&De(d,t,5,l)}}else De(r,t,5,[s])};return n.value=e,n.attached=ma(),n}const To=e=>e.charCodeAt(0)===111&&e.charCodeAt(1)===110&&e.charCodeAt(2)>96&&e.charCodeAt(2)<123,ba=(e,t,n,s,r,o)=>{const i=r==="svg";t==="class"?sa(e,s,i):t==="style"?la(e,n,s):cn(t)?un(t)||fa(e,t,n,s,o):(t[0]==="."?(t=t.slice(1),!0):t[0]==="^"?(t=t.slice(1),!1):ya(e,t,s,i))?(ko(e,t,s),!e.tagName.includes("-")&&(t==="value"||t==="checked"||t==="selected")&&So(e,t,s,i,o,t!=="value")):e._isVueCE&&(xa(e,t)||e._def.__asyncLoader&&(/[A-Z]/.test(t)||!re(s)))?ko(e,Ne(t),s,o,t):(t==="true-value"?e._trueValue=s:t==="false-value"&&(e._falseValue=s),So(e,t,s,i))};function ya(e,t,n,s){if(s)return!!(t==="innerHTML"||t==="textContent"||t in e&&To(t)&&F(n));if(t==="spellcheck"||t==="draggable"||t==="translate"||t==="autocorrect"||t==="sandbox"&&e.tagName==="IFRAME"||t==="form"||t==="list"&&e.tagName==="INPUT"||t==="type"&&e.tagName==="TEXTAREA")return!1;if(t==="width"||t==="height"){const r=e.tagName;if(r==="IMG"||r==="VIDEO"||r==="CANVAS"||r==="SOURCE")return!1}return To(t)&&re(n)?!1:t in e}function xa(e,t){const n=e._def.props;if(!n)return!1;const s=Ne(t);return Array.isArray(n)?n.some(r=>Ne(r)===s):Object.keys(n).some(r=>Ne(r)===s)}const Hn=e=>{const t=e.props["onUpdate:modelValue"]||!1;return P(t)?n=>pn(t,n):t};function _a(e){e.target.composing=!0}function $o(e){const t=e.target;t.composing&&(t.composing=!1,t.dispatchEvent(new Event("input")))}const wt=Symbol("_assign"),Un=Symbol("_initialValue");function Ps(e,t,n){return t&&(e=e.trim()),n&&(e=hn(e)),e}const xe={created(e,{modifiers:{lazy:t,trim:n,number:s}},r){e.parentNode&&(e.type==="text"?e[Un]=e.defaultValue.replace(/[\r\n]/g,""):e.type==="textarea"&&(e[Un]=e.defaultValue.replace(/\r\n?/g,`
`))),e[wt]=Hn(r);const o=s||r.props&&r.props.type==="number";_t(e,t?"change":"input",i=>{i.target.composing||e[wt](Ps(e.value,n,o))}),(n||o)&&_t(e,"change",()=>{e.value=Ps(e.value,n,o)}),t||(_t(e,"compositionstart",_a),_t(e,"compositionend",$o),_t(e,"change",$o))},mounted(e,{value:t,modifiers:{trim:n,number:s}}){const r=t??"",o=e[Un];delete e[Un],o!==void 0&&(e.type==="text"||e.type==="textarea")&&e.value!==o?e[wt](Ps(e.value,n,s)):e.value=r},beforeUpdate(e,{value:t,oldValue:n,modifiers:{lazy:s,trim:r,number:o}},i){if(e[wt]=Hn(i),e.composing)return;const l=(o||e.type==="number")&&!/^0\d/.test(e.value)?hn(e.value):e.value,a=t??"";if(l===a)return;const d=e.getRootNode();(d instanceof Document||d instanceof ShadowRoot)&&d.activeElement===e&&e.type!=="range"&&(s&&t===n||r&&e.value.trim()===a)||(e.value=a)}},Eo={deep:!0,created(e,{value:t,modifiers:{number:n}},s){e._modelValue=t,_t(e,"change",()=>{const r=Array.prototype.filter.call(e.options,o=>o.selected).map(o=>n?hn(Bn(o)):Bn(o));e[wt](e.multiple?fn(e._modelValue)?new Set(r):r:r[0]),e._assigning=!0,Tn(()=>{e._assigning=!1})}),e[wt]=Hn(s)},mounted(e,{value:t}){Ao(e,t)},beforeUpdate(e,{value:t},n){e._modelValue=t,e[wt]=Hn(n)},updated(e,{value:t}){e._assigning||Ao(e,t)}};function Ao(e,t){const n=e.multiple,s=P(t);if(!(n&&!s&&!fn(t))){for(let r=0,o=e.options.length;r<o;r++){const i=e.options[r],l=Bn(i);if(n)if(s){const a=typeof l;a==="string"||a==="number"?i.selected=t.some(d=>String(d)===String(l)):i.selected=fi(t,l)>-1}else i.selected=t.has(l);else if(Dt(Bn(i),t)){e.selectedIndex!==r&&(e.selectedIndex=r);return}}!n&&e.selectedIndex!==-1&&(e.selectedIndex=-1)}}function Bn(e){return"_value"in e?e._value:e.value}const wa=["ctrl","shift","alt","meta"],Sa={stop:e=>e.stopPropagation(),prevent:e=>e.preventDefault(),self:e=>e.target!==e.currentTarget,ctrl:e=>!e.ctrlKey,shift:e=>!e.shiftKey,alt:e=>!e.altKey,meta:e=>!e.metaKey,left:e=>"button"in e&&e.button!==0,middle:e=>"button"in e&&e.button!==1,right:e=>"button"in e&&e.button!==2,exact:(e,t)=>wa.some(n=>e[`${n}Key`]&&!t.includes(n))},zn=(e,t)=>{if(!e)return e;const n=e._withMods||(e._withMods={}),s=t.join(".");return n[s]||(n[s]=((r,...o)=>{for(let i=0;i<t.length;i++){const l=Sa[t[i]];if(l&&l(r,t))return}return e(r,...o)}))},ka=de({patchProp:ba},ta);let Ro;function Ca(){return Ro||(Ro=Ml(ka))}const Ta=((...e)=>{const t=Ca().createApp(...e),{mount:n}=t;return t.mount=s=>{const r=Ea(s);if(!r)return;const o=t._component;!F(o)&&!o.render&&!o.template&&(o.template=r.innerHTML),r.nodeType===1&&(r.textContent="");const i=n(r,!1,$a(r));return r instanceof Element&&(r.removeAttribute("v-cloak"),r.setAttribute("data-v-app","")),i},t});function $a(e){if(e instanceof SVGElement)return"svg";if(typeof MathMLElement=="function"&&e instanceof MathMLElement)return"mathml"}function Ea(e){return re(e)?document.querySelector(e):e}const Ns="secrets.faros.sh",Wn="secrets_faros_sh",St="v1alpha1",Gn="default",Io="token",Vs="-vault-token";let Fs=null,Ds=null;function Tu(e){}function Aa(e){Fs=e||null}function Ra(e){Ds=e||null}async function rn(e,t){if(!Ds)throw{reason:"TenantMissing",message:"no workspace selected"};const n={"Content-Type":"application/json",Accept:"application/json"};Fs&&(n.Authorization="Bearer "+Fs);const s=await fetch("/graphql/"+Ds,{method:"POST",credentials:"same-origin",headers:n,body:JSON.stringify({query:e,variables:t})}),r=await s.text();if(!s.ok)throw{reason:s.status===404?"NotFound":"HTTPError",message:r||s.statusText};const o=r?JSON.parse(r):{};if(o.errors&&o.errors.length)throw{reason:"GraphQLError",message:o.errors.map(i=>i.message).join("; ")};return o.data??{}}function Ls(e,t){return(e.status?.conditions??[]).some(n=>n.type===t&&n.status==="True")}function Ks(e,t){const n=(e.status?.conditions??[]).find(s=>s.type===t);if(!(!n||n.status==="True"))return n.message||n.reason}function Mo(e){return(e.status?.conditions??[]).map(t=>({type:t.type,status:t.status,reason:t.reason,message:t.message,lastTransitionTime:t.lastTransitionTime}))}function Oo(e){const t=e.spec??{},n=e.status??{},s=t.vault??{},r=t.secretRef??{};return{name:e.metadata.name,backend:String(t.backend??""),address:s.address?String(s.address):"",mount:s.mount?String(s.mount):void 0,vaultNamespace:s.namespace?String(s.namespace):void 0,secretName:String(r.name??""),secretNamespace:r.namespace?String(r.namespace):void 0,secretKey:r.key?String(r.key):void 0,backendVersion:n.backendVersion?String(n.backendVersion):void 0,validated:Ls(e,"Validated"),ready:Ls(e,"Ready"),message:Ks(e,"Validated")??Ks(e,"Ready"),creationTimestamp:e.metadata.creationTimestamp,generation:typeof e.metadata.generation=="number"?e.metadata.generation:void 0,observedGeneration:typeof n.observedGeneration=="number"?n.observedGeneration:void 0,conditions:Mo(e)}}function Po(e){const t=e.spec??{},n=e.status??{},s=t.storeRef??{},r=t.target??{},o=Array.isArray(t.data)?t.data:[],i=Array.isArray(t.dataFrom)?t.dataFrom:[];return{name:e.metadata.name,namespace:e.metadata.namespace??"",store:String(s.name??""),targetSecret:String(n.secretName||r.name||e.metadata.name),refreshInterval:String(t.refreshInterval??"1h"),dataFrom:i.map(l=>String(l.path??"")),data:o.map(l=>{const a=l.remoteRef??{};return{secretKey:String(l.secretKey??""),path:String(a.path??""),property:a.property?String(a.property):void 0}}),syncedKeys:typeof n.syncedKeys=="number"?n.syncedKeys:void 0,syncedVersion:n.syncedVersion?String(n.syncedVersion):void 0,lastSyncTime:n.lastSyncTime?String(n.lastSyncTime):void 0,ready:Ls(e,"Ready"),message:Ks(e,"Ready"),creationTimestamp:e.metadata.creationTimestamp,generation:typeof e.metadata.generation=="number"?e.metadata.generation:void 0,observedGeneration:typeof n.observedGeneration=="number"?n.observedGeneration:void 0,conditions:Mo(e)}}function No(e){return e.toLowerCase().replace(/[^a-z0-9-]+/g,"-").replace(/^-+|-+$/g,"").slice(0,253)||"x"}async function js(e){const n=(await rn("mutation($y: String!) { applyYaml(yaml: $y) }",{y:JSON.stringify(e)})).applyYaml;return typeof n=="string"?JSON.parse(n||"{}"):n??{}}async function Ia(e,t){await rn("mutation($n: String!, $ns: String!) { v1 { deleteSecret(name: $n, namespace: $ns) } }",{n:e,ns:t})}const Vo="conditions { type status reason message lastTransitionTime }",Ma=`metadata { name uid resourceVersion generation creationTimestamp } spec { backend vault { address mount namespace } secretRef { name namespace key } } status { observedGeneration backendVersion ${Vo} }`,Oa=`metadata { name namespace uid resourceVersion generation creationTimestamp } spec { storeRef { name } refreshInterval target { name } data { secretKey remoteRef { path property } } dataFrom { path property } } status { observedGeneration secretName lastSyncTime syncedVersion syncedKeys ${Vo} }`;async function Fo(e,t){const n=`query { ${Wn} { ${St} { ${e} { items { ${t} } } } } }`;return(await rn(n,{}))[Wn]?.[St]?.[e]?.items??[]}const kt={async listStores(){return(await Fo("SecretStores",Ma)).map(Oo)},async createStore(e){const t=No(e.name),n={address:e.address};e.mount&&(n.mount=e.mount),e.vaultNamespace&&(n.namespace=e.vaultNamespace);const s=e.credential.mode==="token",r=s?{name:t+Vs,namespace:Gn,key:Io}:{name:e.credential.mode==="existing"?e.credential.secretName:"",...e.credential.mode==="existing"&&e.credential.secretNamespace?{namespace:e.credential.secretNamespace}:{},...e.credential.mode==="existing"&&e.credential.secretKey?{key:e.credential.secretKey}:{}},o=await js({apiVersion:`${Ns}/${St}`,kind:"SecretStore",metadata:{name:t},spec:{backend:"vault",vault:n,secretRef:r}});return s&&e.credential.mode==="token"&&await js({apiVersion:"v1",kind:"Secret",metadata:{name:t+Vs,namespace:Gn,ownerReferences:[{apiVersion:`${Ns}/${St}`,kind:"SecretStore",name:t,uid:o.metadata.uid}]},type:"Opaque",stringData:{[Io]:e.credential.token}}),Oo(o)},async deleteStore(e){if(await rn(`mutation($n: String!) { ${Wn} { ${St} { deleteSecretStore(name: $n) } } }`,{n:e.name}),e.secretName===e.name+Vs)try{await Ia(e.secretName,e.secretNamespace||Gn)}catch(t){if(!/not\s*found/i.test(t.message??""))throw t}},async listSynced(){return(await Fo("SyncedSecrets",Oa)).map(Po)},async createSynced(e){const t=No(e.name),n={storeRef:{name:e.store}};e.refreshInterval&&(n.refreshInterval=e.refreshInterval),e.targetName&&(n.target={name:e.targetName});const s=e.dataFrom.map(i=>i.trim()).filter(i=>i);s.length&&(n.dataFrom=s.map(i=>({path:i})));const r=e.data.filter(i=>i.secretKey.trim()&&i.path.trim());r.length&&(n.data=r.map(i=>({secretKey:i.secretKey.trim(),remoteRef:{path:i.path.trim(),...i.property?.trim()?{property:i.property.trim()}:{}}})));const o=await js({apiVersion:`${Ns}/${St}`,kind:"SyncedSecret",metadata:{name:t,namespace:e.namespace||Gn},spec:n});return Po(o)},async deleteSynced(e,t){await rn(`mutation($n: String!, $ns: String!) { ${Wn} { ${St} { deleteSyncedSecret(name: $n, namespace: $ns) } } }`,{n:e,ns:t})}};function Do(e){if(!e)return"—";const t=Date.parse(e);if(Number.isNaN(t))return"—";const n=Math.max(0,Math.floor((Date.now()-t)/1e3));if(n<60)return`${n}s`;const s=Math.floor(n/60);if(s<60)return`${s}m`;const r=Math.floor(s/60);return r<24?`${r}h`:`${Math.floor(r/24)}d`}function Pa(e){return e?(e.startsWith("sha256:")?e.slice(7):e).slice(0,8):"—"}function Na(e){if(!e)return"—";const t=Date.parse(e);return Number.isNaN(t)?e:new Date(t).toLocaleString(void 0,{year:"numeric",month:"short",day:"numeric",hour:"2-digit",minute:"2-digit"})}const le=Bt({open:!1,title:"",message:"",confirmLabel:"Confirm",cancelLabel:"Cancel",danger:!1,resolve:null});function Lo(e){return le.resolve&&(le.resolve(!1),le.resolve=null),le.title=e.title,le.message=e.message??"",le.confirmLabel=e.confirmLabel??"Confirm",le.cancelLabel=e.cancelLabel??"Cancel",le.danger=e.danger??!1,le.open=!0,new Promise(t=>{le.resolve=t})}function Ko(e){le.open=!1;const t=le.resolve;le.resolve=null,t&&t(e)}/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Va=e=>e.replace(/([a-z0-9])([A-Z])/g,"$1-$2").toLowerCase();/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */var qn={xmlns:"http://www.w3.org/2000/svg",width:24,height:24,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor","stroke-width":2,"stroke-linecap":"round","stroke-linejoin":"round"};/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Fa=({size:e,strokeWidth:t=2,absoluteStrokeWidth:n,color:s,iconNode:r,name:o,class:i,...l},{slots:a})=>jn("svg",{...qn,width:e||qn.width,height:e||qn.height,stroke:s||qn.stroke,"stroke-width":n?Number(t)*24/Number(e):t,class:["lucide",`lucide-${Va(o??"icon")}`],...l},[...r.map(d=>jn(...d)),...a.default?[a.default()]:[]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const it=(e,t)=>(n,{slots:s})=>jn(Fa,{...n,iconNode:t,name:e},s);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const jo=it("CircleAlertIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["line",{x1:"12",x2:"12",y1:"8",y2:"12",key:"1pkeuh"}],["line",{x1:"12",x2:"12.01",y1:"16",y2:"16",key:"4dfq90"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Ho=it("CircleCheckBigIcon",[["path",{d:"M21.801 10A10 10 0 1 1 17 3.335",key:"yps3ct"}],["path",{d:"m9 11 3 3L22 4",key:"1pflzl"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Da=it("CircleXIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m15 9-6 6",key:"1uzhvr"}],["path",{d:"m9 9 6 6",key:"z0biqf"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Uo=it("CircleIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Bo=it("ClockIcon",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["polyline",{points:"12 6 12 12 16 14",key:"68esgv"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const La=it("InboxIcon",[["polyline",{points:"22 12 16 12 14 15 10 15 8 12 2 12",key:"o97t9d"}],["path",{d:"M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z",key:"oot6mr"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Ka=it("LoaderCircleIcon",[["path",{d:"M21 12a9 9 0 1 1-6.219-8.56",key:"13zald"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const ja=it("Trash2Icon",[["path",{d:"M3 6h18",key:"d0wm0j"}],["path",{d:"M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6",key:"4alrt4"}],["path",{d:"M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2",key:"v07s0e"}],["line",{x1:"10",x2:"10",y1:"11",y2:"17",key:"1uufr5"}],["line",{x1:"14",x2:"14",y1:"11",y2:"17",key:"xtxkd"}]]);/**
 * @license lucide-vue-next v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const zo=it("TriangleAlertIcon",[["path",{d:"m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3",key:"wmoenq"}],["path",{d:"M12 9v4",key:"juzpu7"}],["path",{d:"M12 17h.01",key:"p32p05"}]]),Ha=["aria-busy"],Ua={class:"resource-table-live",role:"status","aria-live":"polite","aria-atomic":"true",style:{"block-size":"1px",clip:"rect(0 0 0 0)","clip-path":"inset(50%)","inline-size":"1px",margin:"-1px",overflow:"hidden",padding:"0",position:"absolute","white-space":"nowrap"}},Ba={key:0,class:"resource-table-error",role:"alert","aria-live":"assertive"},za={class:"resource-table-error-message"},Wa={key:1,class:"resource-table-loading",role:"status","aria-live":"polite","aria-label":"Loading resources"},Ga={key:0,class:"resource-table-stale",role:"alert","aria-live":"assertive"},qa={class:"resource-table-error-message"},Ja={class:"resource-table-table"},Ya={class:"resource-table-head-row"},Xa=["onClick"],Qa={key:0},Za=["colspan"],ec={class:"resource-table-empty-label"},Hs=ct({__name:"ResourceTable",props:{columns:{},rows:{},rowKey:{},loaded:{type:[Boolean,null],default:null},loading:{type:Boolean},error:{},stale:{type:Boolean,default:!1},retryable:{type:Boolean,default:!1},emptyText:{default:"No data"},interactive:{type:Boolean,default:!0}},emits:["rowClick","retry"],setup(e,{emit:t}){const n=e,s=ye(()=>n.loaded!==null),r=ye(()=>s.value?n.loaded===!1&&!!n.error:!!n.error),o=ye(()=>s.value?n.loaded===!1:!!n.loading),i=ye(()=>s.value?!!n.loading&&!(n.loaded===!1&&n.error)||n.loaded===!1&&!n.error:!!n.loading),l=t;function a(u){n.interactive&&l("rowClick",u)}function d(u,p){if(typeof n.rowKey=="function")return n.rowKey(u,p);if(typeof n.rowKey=="string"){const x=u[n.rowKey];if(typeof x=="string"||typeof x=="number")return x}for(const x of["name","id","uid"]){const k=u[x];if(typeof k=="string"||typeof k=="number")return k}return p}return(u,p)=>(M(),D("div",{class:"resource-table","aria-busy":i.value},[h("span",Ua,H(s.value&&e.loading&&e.loaded?"Updating…":""),1),r.value?(M(),D("div",Ba,[X(Ce(jo),{class:"resource-table-error-icon","stroke-width":1.75}),h("span",za,H(e.error),1),e.retryable?(M(),D("button",{key:0,class:"resource-table-retry",type:"button",onClick:p[0]||(p[0]=x=>l("retry"))},"Retry")):be("",!0)])):o.value?(M(),D("div",Wa,[p[3]||(p[3]=h("div",{class:"resource-table-loading-head"},[h("div",{class:"shimmer resource-table-skeleton resource-table-skeleton-short"})],-1)),(M(),D(se,null,ut(5,x=>h("div",{key:x,class:"resource-table-loading-row"},[...p[2]||(p[2]=[h("div",{class:"shimmer resource-table-skeleton resource-table-skeleton-wide"},null,-1),h("div",{class:"shimmer resource-table-skeleton resource-table-skeleton-mid"},null,-1),h("div",{class:"shimmer resource-table-skeleton resource-table-skeleton-small"},null,-1)])])),64))])):(M(),D(se,{key:2},[s.value&&e.error?(M(),D("div",Ga,[X(Ce(jo),{class:"resource-table-error-icon","stroke-width":1.75}),h("span",qa,H(e.stale?"Showing the last successful result. ":"")+H(e.error),1),e.retryable?(M(),D("button",{key:0,class:"resource-table-retry",type:"button",onClick:p[1]||(p[1]=x=>l("retry"))},"Retry")):be("",!0)])):be("",!0),h("table",Ja,[h("thead",null,[h("tr",Ya,[(M(!0),D(se,null,ut(e.columns,x=>(M(),D("th",{key:x.key,class:"resource-table-heading"},H(x.label),1))),128))])]),h("tbody",null,[(M(!0),D(se,null,ut(e.rows,(x,k)=>(M(),D("tr",{key:d(x,k),class:Ie(["stagger-item resource-table-row",{"is-interactive":e.interactive}]),style:mn({animationDelay:`${k*35}ms`}),onClick:L=>a(x)},[(M(!0),D(se,null,ut(e.columns,L=>(M(),D("td",{key:L.key,class:"resource-table-cell"},[ul(u.$slots,L.key,{value:x[L.key],row:x},()=>[Zt(H(x[L.key]),1)])]))),128))],14,Xa))),128)),e.rows.length===0?(M(),D("tr",Qa,[h("td",{colspan:e.columns.length,class:"resource-table-empty-cell"},[X(Ce(La),{class:"resource-table-empty-icon","stroke-width":1.25}),h("p",ec,H(e.emptyText),1)],8,Za)])):be("",!0)])])],64))],8,Ha))}}),Wo=`/* CANONICAL SOURCE — provider-sdk/portalkit-vue. This stylesheet is imported
 * as text and injected by ResourceTableDeleteButton.vue because standalone
 * provider portals load only their IIFE main.js; Vite's extracted SFC CSS
 * asset is not loaded by the host portal. */
.pk-resource-delete {
  align-items: center;
  background: transparent;
  border: 0;
  border-radius: 6px;
  color: color-mix(in srgb, var(--color-text-muted) 40%, transparent);
  cursor: pointer;
  display: inline-flex;
  height: 28px;
  justify-content: center;
  opacity: 0;
  padding: 0;
  transition: background-color 0.12s ease, color 0.12s ease, opacity 0.12s ease;
  width: 28px;
}
.pk-resource-delete.is-busy,
.pk-resource-delete:focus-visible,
.resource-table-row:hover .pk-resource-delete,
.resource-table-row:focus-within .pk-resource-delete {
  opacity: 1;
}
.pk-resource-delete:hover,
.pk-resource-delete:focus-visible {
  background: var(--color-danger-subtle);
  color: var(--color-danger);
}
.pk-resource-delete:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
}
.pk-resource-delete:disabled {
  cursor: progress;
}
.pk-resource-delete.is-busy {
  color: var(--color-danger);
}
.pk-resource-delete-icon {
  height: 14px;
  width: 14px;
}
.pk-resource-delete-icon.is-spinning {
  animation: pk-resource-delete-spin 0.8s linear infinite;
}
@keyframes pk-resource-delete-spin {
  to { transform: rotate(360deg); }
}
@media (prefers-reduced-motion: reduce) {
  .pk-resource-delete-icon.is-spinning { animation: none; }
}
@media (hover: none) {
  .pk-resource-delete { opacity: 1; }
}
`,tc=["title","aria-label","aria-busy","disabled"],Go="faros-portalkit-resource-table-delete-css",qo=ct({__name:"ResourceTableDeleteButton",props:{label:{},busyLabel:{default:"Deleting…"},busy:{type:Boolean,default:!1},disabled:{type:Boolean,default:!1}},emits:["click"],setup(e,{emit:t}){if(typeof document<"u"){let o=document.getElementById(Go);o||(o=document.createElement("style"),o.id=Go,document.head.appendChild(o)),o.textContent!==Wo&&(o.textContent=Wo)}const n=e,s=ye(()=>n.busy?n.busyLabel:n.label),r=t;return(o,i)=>(M(),D("button",{class:Ie(["pk-resource-delete",{"is-busy":e.busy}]),type:"button",title:s.value,"aria-label":s.value,"aria-busy":e.busy||void 0,disabled:e.disabled||e.busy,onClick:i[0]||(i[0]=zn(l=>r("click",l),["stop"]))},[e.busy?(M(),xt(Ce(Ka),{key:0,class:"pk-resource-delete-icon is-spinning","stroke-width":1.75,"aria-hidden":"true"})):(M(),xt(Ce(ja),{key:1,class:"pk-resource-delete-icon","stroke-width":1.75,"aria-hidden":"true"}))],10,tc))}}),nc={class:"status-badge-dot-wrap"},Jn=ct({__name:"StatusBadge",props:{status:{},connected:{type:[Boolean,null],default:null},tone:{default:null}},setup(e){const t=e,n={success:{toneClass:"tone-success",dotClass:"dot-success",pulseClass:"pulse-success"},warning:{toneClass:"tone-warning",dotClass:"dot-warning",pulseClass:"pulse-warning"},danger:{toneClass:"tone-danger",dotClass:"dot-danger",pulseClass:"pulse-danger"},muted:{toneClass:"tone-muted",dotClass:"dot-muted",pulseClass:"pulse-muted"}},s=ye(()=>{if(t.connected===!1)return{...n.danger,icon:Da};if(t.tone)return{...n[t.tone],icon:t.tone==="danger"?zo:t.tone==="warning"?Bo:t.tone==="success"?Ho:Uo};switch(t.status?.toLowerCase()){case"ready":case"succeeded":case"committed":case"active":case"loaded":return{...n.success,icon:Ho};case"scheduling":case"pending":case"provisioning":case"running":case"retrying":case"status unavailable":case"loading":case"starting":case"loaded unverified":return{...n.warning,icon:Bo};case"terminating":case"failed":case"error":case"repository missing":case"connection missing":case"needs attention":return{...n.danger,icon:zo};default:return{...n.muted,icon:Uo}}});return(r,o)=>(M(),D("span",{class:Ie(["status-badge",s.value.toneClass])},[h("span",nc,[e.status?.toLowerCase()==="ready"&&e.connected!==!1?(M(),D("span",{key:0,class:Ie(["live-dot status-badge-pulse",s.value.pulseClass])},null,2)):be("",!0),h("span",{class:Ie(["status-badge-dot",s.value.dotClass])},null,2)]),Zt(" "+H(e.status),1)],2))}}),sc={class:"conditions-panel"},rc={key:0,class:"conditions-stale"},oc={class:"conditions-type"},ic={class:"conditions-message"},lc={class:"conditions-muted"},Jo=ct({__name:"ConditionsPanel",props:{conditions:{},generation:{},observedGeneration:{},emptyText:{}},setup(e){const t=e,n=ye(()=>t.observedGeneration===void 0||t.generation===void 0||t.observedGeneration>=t.generation),s=ye(()=>t.conditions.map(o=>({...o,reasonLabel:o.reason||"-",messageLabel:o.message||"-",sinceLabel:o.lastTransitionTime||"-"})));function r(o){return o==="True"?"success":o==="False"?"warning":"muted"}return(o,i)=>(M(),D("div",sc,[i[0]||(i[0]=h("h3",{class:"conditions-title"},"Conditions",-1)),e.observedGeneration!==void 0&&!n.value?(M(),D("p",rc," Controller has not caught up - spec generation "+H(e.generation)+", observed "+H(e.observedGeneration)+". ",1)):be("",!0),X(Hs,{columns:[{key:"type",label:"Type"},{key:"status",label:"Status"},{key:"reasonLabel",label:"Reason"},{key:"messageLabel",label:"Message"},{key:"sinceLabel",label:"Since"}],rows:s.value,interactive:!1,"empty-text":e.emptyText||"No conditions yet. The controller has not reconciled this resource."},{type:ie(({value:l})=>[h("span",oc,H(l),1)]),status:ie(({value:l})=>[X(Jn,{status:String(l),tone:r(String(l))},null,8,["status","tone"])]),messageLabel:ie(({value:l})=>[h("span",ic,H(l),1)]),sinceLabel:ie(({value:l})=>[h("span",lc,H(l),1)]),_:1},8,["rows","empty-text"])]))}}),ac={class:"page"},cc={class:"page-head"},uc={class:"actions"},fc={key:0,class:"panel"},dc={class:"field"},pc={class:"field"},hc={class:"field"},gc={class:"field"},mc={class:"field"},vc={class:"field"},bc={class:"field"},yc={class:"field"},xc={class:"field"},_c={class:"actions"},wc=["disabled"],Sc={key:0,class:"error"},kc={class:"mono"},Cc={class:"mono"},Tc={class:"mono"},$c={class:"mono"},Ec={key:1,class:"panel"},Ac={class:"panel-head"},Rc={class:"panel-title"},Ic={key:0,class:"muted"},Mc=ct({__name:"StoresView",setup(e){const t=B([]),n=B(null),s=B(!1),r=B(!1),o=B(null),i=B(null),l=ye(()=>t.value.find(oe=>oe.name===i.value)??null),a=B(!1),d=B(""),u=B(""),p=B(""),x=B(""),k=B("token"),L=B(""),A=B(""),z=B(""),W=B(""),U=B(!1),K=B(null);let R;function ne(){d.value=u.value=p.value=x.value="",L.value=A.value=z.value=W.value="",k.value="token",K.value=null}async function fe(){s.value=!0;try{t.value=await kt.listStores(),n.value=null,r.value=!0}catch(oe){const E=oe;n.value=E.reason==="TenantMissing"?null:`${E.reason}: ${E.message}`}finally{s.value=!1}}async function $e(){if(K.value=null,!d.value||!u.value){K.value="name and vault address are required";return}if(k.value==="token"&&!L.value){K.value="paste a vault token or reference an existing secret";return}if(k.value==="existing"&&!A.value){K.value="the credential secret name is required";return}U.value=!0;try{await kt.createStore({name:d.value,address:u.value,mount:p.value||void 0,vaultNamespace:x.value||void 0,credential:k.value==="token"?{mode:"token",token:L.value}:{mode:"existing",secretName:A.value,secretNamespace:z.value||void 0,secretKey:W.value||void 0}}),ne(),a.value=!1,await fe()}catch(oe){const E=oe;K.value=`${E.reason}: ${E.message}`}finally{U.value=!1}}async function Oe(oe){if(await Lo({title:`Delete store "${oe.name}"?`,message:"SyncedSecrets referencing it will stop syncing.",confirmLabel:"Delete",danger:!0})){o.value=oe.name;try{await kt.deleteStore(oe),i.value===oe.name&&(i.value=null),await fe()}catch(j){const dt=j;n.value=`${dt.reason}: ${dt.message}`}finally{o.value=null}}}function ft(oe){const E=String(oe.name);i.value=i.value===E?null:E}const ke=[{key:"name",label:"Name"},{key:"backend",label:"Backend"},{key:"address",label:"Address"},{key:"validated",label:"Validated"},{key:"ready",label:"Ready"},{key:"backendVersion",label:"Version"},{key:"age",label:"Age"},{key:"actions",label:""}],lt=ye(()=>t.value.map(oe=>({...oe,age:Do(oe.creationTimestamp)})));return xs(()=>{fe(),R=window.setInterval(fe,5e3)}),Mn(()=>window.clearInterval(R)),(oe,E)=>(M(),D("section",ac,[h("header",cc,[E[12]||(E[12]=h("div",null,[h("h2",{class:"page-title"},"Secret stores"),h("p",{class:"page-meta"}," A store binds this workspace to one external secret backend (Vault). Synced secrets read through it; the external store stays the source of truth. ")],-1)),h("div",uc,[h("button",{class:"primary",onClick:E[0]||(E[0]=j=>a.value=!a.value)},H(a.value?"Cancel":"Add store"),1)])]),a.value?(M(),D("div",fc,[E[24]||(E[24]=h("h3",{class:"panel-title"},"New secret store",-1)),h("form",{class:"form",onSubmit:zn($e,["prevent"])},[h("div",dc,[E[13]||(E[13]=h("span",{class:"field-label"},"Name",-1)),ue(h("input",{"onUpdate:modelValue":E[1]||(E[1]=j=>d.value=j),placeholder:"prod-vault",autocomplete:"off"},null,512),[[xe,d.value]])]),h("div",pc,[E[14]||(E[14]=h("span",{class:"field-label"},"Vault address",-1)),ue(h("input",{"onUpdate:modelValue":E[2]||(E[2]=j=>u.value=j),placeholder:"https://vault.example.com:8200",autocomplete:"off"},null,512),[[xe,u.value]])]),h("div",hc,[E[15]||(E[15]=h("span",{class:"field-label"},'Mount (KV v2, optional — defaults to "secret")',-1)),ue(h("input",{"onUpdate:modelValue":E[3]||(E[3]=j=>p.value=j),placeholder:"secret",autocomplete:"off"},null,512),[[xe,p.value]])]),h("div",gc,[E[16]||(E[16]=h("span",{class:"field-label"},"Vault namespace (Enterprise, optional)",-1)),ue(h("input",{"onUpdate:modelValue":E[4]||(E[4]=j=>x.value=j),placeholder:"",autocomplete:"off"},null,512),[[xe,x.value]])]),h("div",mc,[E[18]||(E[18]=h("span",{class:"field-label"},"Credential",-1)),ue(h("select",{"onUpdate:modelValue":E[5]||(E[5]=j=>k.value=j)},[...E[17]||(E[17]=[h("option",{value:"token"},"Paste a Vault token (stored as a new Secret)",-1),h("option",{value:"existing"},"Reference an existing Secret",-1)])],512),[[Eo,k.value]])]),k.value==="token"?(M(),D(se,{key:0},[h("div",vc,[E[19]||(E[19]=h("span",{class:"field-label"},"Vault token",-1)),ue(h("input",{"onUpdate:modelValue":E[6]||(E[6]=j=>L.value=j),type:"password",placeholder:"hvs.…",autocomplete:"off"},null,512),[[xe,L.value]])]),E[20]||(E[20]=h("p",{class:"muted"},"The token is stored as a Secret in your workspace, owned by the store so it is cleaned up with it. The provider validates it and reports the result below.",-1))],64)):(M(),D(se,{key:1},[h("div",bc,[E[21]||(E[21]=h("span",{class:"field-label"},"Secret name",-1)),ue(h("input",{"onUpdate:modelValue":E[7]||(E[7]=j=>A.value=j),placeholder:"vault-credentials",autocomplete:"off"},null,512),[[xe,A.value]])]),h("div",yc,[E[22]||(E[22]=h("span",{class:"field-label"},'Secret namespace (optional — defaults to "default")',-1)),ue(h("input",{"onUpdate:modelValue":E[8]||(E[8]=j=>z.value=j),placeholder:"default",autocomplete:"off"},null,512),[[xe,z.value]])]),h("div",xc,[E[23]||(E[23]=h("span",{class:"field-label"},'Secret key (optional — defaults to "token")',-1)),ue(h("input",{"onUpdate:modelValue":E[9]||(E[9]=j=>W.value=j),placeholder:"token",autocomplete:"off"},null,512),[[xe,W.value]])])],64)),h("div",_c,[h("button",{class:"primary",type:"submit",disabled:U.value},H(U.value?"Creating…":"Create"),9,wc),h("button",{class:"secondary",type:"button",onClick:E[10]||(E[10]=()=>{a.value=!1,ne()})},"Cancel"),K.value?(M(),D("span",Sc,H(K.value),1)):be("",!0)])],32)])):be("",!0),X(Hs,{columns:ke,rows:lt.value,loaded:r.value,loading:s.value,error:n.value,stale:!!n.value&&t.value.length>0,retryable:"","empty-text":"No secret stores yet. Add one to connect this workspace to Vault.",onRowClick:ft,onRetry:fe},{name:ie(({value:j})=>[h("span",kc,H(j),1)]),backend:ie(({value:j})=>[h("span",Cc,H(j),1)]),address:ie(({value:j})=>[h("span",Tc,H(j||"—"),1)]),validated:ie(({row:j})=>[X(Jn,{status:j.validated?"validated":"pending",tone:j.validated?"success":"warning"},null,8,["status","tone"])]),ready:ie(({row:j})=>[X(Jn,{status:j.ready?"ready":"pending"},null,8,["status"])]),backendVersion:ie(({value:j})=>[h("span",$c,H(j||"—"),1)]),actions:ie(({row:j})=>[X(qo,{label:"Delete store",busy:o.value===j.name,onClick:dt=>Oe(j)},null,8,["busy","onClick"])]),_:1},8,["rows","loaded","loading","error","stale"]),l.value?(M(),D("div",Ec,[h("div",Ac,[h("h3",Rc,H(l.value.name)+" — conditions",1),h("button",{class:"link",onClick:E[11]||(E[11]=j=>i.value=null)},"Close")]),l.value.message?(M(),D("p",Ic,H(l.value.message),1)):be("",!0),X(Jo,{conditions:l.value.conditions,generation:l.value.generation,"observed-generation":l.value.observedGeneration},null,8,["conditions","generation","observed-generation"])])):be("",!0)]))}}),Oc={class:"page"},Pc={class:"page-head"},Nc={class:"actions"},Vc={key:0,class:"panel"},Fc={class:"field"},Dc={class:"field"},Lc={class:"field"},Kc=["value"],jc={key:0,class:"muted"},Hc={class:"field"},Uc={class:"field"},Bc={class:"field"},zc=["onUpdate:modelValue"],Wc=["onClick","disabled"],Gc={class:"field"},qc=["onUpdate:modelValue"],Jc=["onUpdate:modelValue"],Yc=["onUpdate:modelValue"],Xc=["onClick"],Qc={class:"actions"},Zc=["disabled"],eu={key:0,class:"error"},tu={class:"mono"},nu={class:"mono"},su={class:"mono"},ru={class:"mono"},ou={class:"mono"},iu=["title"],lu={key:1,class:"panel"},au={class:"panel-head"},cu={class:"panel-title"},uu={key:0,class:"muted"},fu=ct({__name:"SyncedSecretsView",setup(e){const t=B([]),n=B([]),s=B(null),r=B(!1),o=B(!1),i=B(null),l=B(null),a=ye(()=>t.value.find(N=>d(N)===l.value)??null);function d(N){return N.namespace+"/"+N.name}const u=B(!1),p=B(""),x=B("default"),k=B(""),L=B("1h"),A=B(""),z=B([""]),W=B([]),U=B(!1),K=B(null);let R;function ne(){p.value=A.value="",x.value="default",k.value=n.value[0]?.name??"",L.value="1h",z.value=[""],W.value=[],K.value=null}function fe(){z.value.push("")}function $e(N){z.value.splice(N,1)}function Oe(){W.value.push({secretKey:"",path:"",property:""})}function ft(N){W.value.splice(N,1)}async function ke(){r.value=!0;try{const[N,T]=await Promise.all([kt.listSynced(),kt.listStores()]);t.value=N,n.value=T,!k.value&&T.length&&(k.value=T[0].name),s.value=null,o.value=!0}catch(N){const T=N;s.value=T.reason==="TenantMissing"?null:`${T.reason}: ${T.message}`}finally{r.value=!1}}async function lt(){if(K.value=null,!p.value||!k.value){K.value="name and store are required";return}const N=z.value.map(_=>_.trim()).filter(_=>_),T=W.value.filter(_=>_.secretKey.trim()&&_.path.trim());if(!N.length&&!T.length){K.value="add at least one path (or key mapping) to sync";return}U.value=!0;try{await kt.createSynced({name:p.value,namespace:x.value||"default",store:k.value,refreshInterval:L.value||void 0,targetName:A.value||void 0,dataFrom:N,data:T}),ne(),u.value=!1,await ke()}catch(_){const ae=_;K.value=`${ae.reason}: ${ae.message}`}finally{U.value=!1}}async function oe(N){if(await Lo({title:`Delete synced secret "${N.name}"?`,message:`The projected Secret "${N.targetSecret}" in namespace "${N.namespace}" is removed with it.`,confirmLabel:"Delete",danger:!0})){i.value=d(N);try{await kt.deleteSynced(N.name,N.namespace),l.value===d(N)&&(l.value=null),await ke()}catch(_){const ae=_;s.value=`${ae.reason}: ${ae.message}`}finally{i.value=null}}}function E(N){const T=String(N.key);l.value=l.value===T?null:T}const j=[{key:"name",label:"Name"},{key:"namespace",label:"Namespace"},{key:"store",label:"Store"},{key:"targetSecret",label:"Target Secret"},{key:"refreshInterval",label:"Refresh"},{key:"syncedKeys",label:"Keys"},{key:"syncedVersion",label:"Version"},{key:"lastSyncTime",label:"Last Sync"},{key:"ready",label:"Ready"},{key:"actions",label:""}],dt=ye(()=>t.value.map(N=>({...N,key:d(N),age:Do(N.creationTimestamp)})));return xs(()=>{ke(),R=window.setInterval(ke,5e3)}),Mn(()=>window.clearInterval(R)),(N,T)=>(M(),D("section",Oc,[h("header",Pc,[T[8]||(T[8]=h("div",null,[h("h2",{class:"page-title"},"Synced secrets"),h("p",{class:"page-meta"}," A synced secret projects material from a store into a workspace Secret on a refresh interval — declare paths and key mappings instead of hand-placing Secrets. ")],-1)),h("div",Nc,[h("button",{class:"primary",onClick:T[0]||(T[0]=_=>u.value=!u.value)},H(u.value?"Cancel":"Add synced secret"),1)])]),u.value?(M(),D("div",Vc,[T[16]||(T[16]=h("h3",{class:"panel-title"},"New synced secret",-1)),h("form",{class:"form",onSubmit:zn(lt,["prevent"])},[h("div",Fc,[T[9]||(T[9]=h("span",{class:"field-label"},"Name",-1)),ue(h("input",{"onUpdate:modelValue":T[1]||(T[1]=_=>p.value=_),placeholder:"db-credentials",autocomplete:"off"},null,512),[[xe,p.value]])]),h("div",Dc,[T[10]||(T[10]=h("span",{class:"field-label"},"Namespace",-1)),ue(h("input",{"onUpdate:modelValue":T[2]||(T[2]=_=>x.value=_),placeholder:"default",autocomplete:"off"},null,512),[[xe,x.value]])]),h("div",Lc,[T[11]||(T[11]=h("span",{class:"field-label"},"Store",-1)),ue(h("select",{"onUpdate:modelValue":T[3]||(T[3]=_=>k.value=_)},[(M(!0),D(se,null,ut(n.value,_=>(M(),D("option",{key:_.name,value:_.name},H(_.name),9,Kc))),128))],512),[[Eo,k.value]]),n.value.length?be("",!0):(M(),D("p",jc,"No secret stores yet — create one on the Stores tab first."))]),h("div",Hc,[T[12]||(T[12]=h("span",{class:"field-label"},'Refresh interval (optional — defaults to "1h")',-1)),ue(h("input",{"onUpdate:modelValue":T[4]||(T[4]=_=>L.value=_),placeholder:"1h",autocomplete:"off"},null,512),[[xe,L.value]])]),h("div",Uc,[T[13]||(T[13]=h("span",{class:"field-label"},"Target Secret name (optional — defaults to the synced secret's name)",-1)),ue(h("input",{"onUpdate:modelValue":T[5]||(T[5]=_=>A.value=_),placeholder:"",autocomplete:"off"},null,512),[[xe,A.value]])]),h("div",Bc,[T[14]||(T[14]=h("span",{class:"field-label"},"Pull whole paths (dataFrom)",-1)),(M(!0),D(se,null,ut(z.value,(_,ae)=>(M(),D("div",{key:"df"+ae,class:"row-line"},[ue(h("input",{"onUpdate:modelValue":Ee=>z.value[ae]=Ee,placeholder:"apps/myapp/db",autocomplete:"off"},null,8,zc),[[xe,z.value[ae]]]),h("button",{class:"danger",type:"button",onClick:Ee=>$e(ae),disabled:z.value.length===1&&!z.value[0]},"Remove",8,Wc)]))),128)),h("div",null,[h("button",{class:"secondary",type:"button",onClick:fe},"Add path")])]),h("div",Gc,[T[15]||(T[15]=h("span",{class:"field-label"},"Key mappings (optional — cherry-pick and rename properties)",-1)),(M(!0),D(se,null,ut(W.value,(_,ae)=>(M(),D("div",{key:"dm"+ae,class:"row-line"},[ue(h("input",{"onUpdate:modelValue":Ee=>_.secretKey=Ee,placeholder:"secret key",autocomplete:"off"},null,8,qc),[[xe,_.secretKey]]),ue(h("input",{"onUpdate:modelValue":Ee=>_.path=Ee,placeholder:"remote path",autocomplete:"off"},null,8,Jc),[[xe,_.path]]),ue(h("input",{"onUpdate:modelValue":Ee=>_.property=Ee,placeholder:"property (optional)",autocomplete:"off"},null,8,Yc),[[xe,_.property]]),h("button",{class:"danger",type:"button",onClick:Ee=>ft(ae)},"Remove",8,Xc)]))),128)),h("div",null,[h("button",{class:"secondary",type:"button",onClick:Oe},"Add mapping")])]),h("div",Qc,[h("button",{class:"primary",type:"submit",disabled:U.value},H(U.value?"Creating…":"Create"),9,Zc),h("button",{class:"secondary",type:"button",onClick:T[6]||(T[6]=()=>{u.value=!1,ne()})},"Cancel"),K.value?(M(),D("span",eu,H(K.value),1)):be("",!0)])],32)])):be("",!0),X(Hs,{columns:j,rows:dt.value,"row-key":"key",loaded:o.value,loading:r.value,error:s.value,stale:!!s.value&&t.value.length>0,retryable:"","empty-text":"No synced secrets yet. Add one to project material from a store.",onRowClick:E,onRetry:ke},{name:ie(({value:_})=>[h("span",tu,H(_),1)]),namespace:ie(({value:_})=>[h("span",nu,H(_),1)]),store:ie(({value:_})=>[h("span",su,H(_),1)]),targetSecret:ie(({value:_})=>[h("span",ru,H(_),1)]),refreshInterval:ie(({value:_})=>[h("span",ou,H(_),1)]),syncedKeys:ie(({value:_})=>[Zt(H(_??"—"),1)]),syncedVersion:ie(({row:_})=>[h("span",{class:"mono",title:String(_.syncedVersion??"")},H(Ce(Pa)(_.syncedVersion)),9,iu)]),lastSyncTime:ie(({value:_})=>[Zt(H(Ce(Na)(_)),1)]),ready:ie(({row:_})=>[X(Jn,{status:_.ready?"ready":"pending"},null,8,["status"])]),actions:ie(({row:_})=>[X(qo,{label:"Delete synced secret",busy:i.value===_.key,onClick:ae=>oe(_)},null,8,["busy","onClick"])]),_:1},8,["rows","loaded","loading","error","stale"]),a.value?(M(),D("div",lu,[h("div",au,[h("h3",cu,H(a.value.namespace)+"/"+H(a.value.name)+" — conditions",1),h("button",{class:"link",onClick:T[7]||(T[7]=_=>l.value=null)},"Close")]),a.value.message?(M(),D("p",uu,H(a.value.message),1)):be("",!0),X(Jo,{conditions:a.value.conditions,generation:a.value.generation,"observed-generation":a.value.observedGeneration},null,8,["conditions","generation","observed-generation"])])):be("",!0)]))}}),du={id:"pk-modal-title",class:"pk-title"},pu={class:"pk-actions"},hu=((e,t)=>{const n=e.__vccOpts||e;for(const[s,r]of t)n[s]=r;return n})(ct({__name:"ConfirmDialog",setup(e){const t=B(null),n=B(null);let s=null;const r=ye(()=>le.message.split(`
`).map(a=>a.trim()).filter(Boolean));function o(){Ko(!0)}function i(){Ko(!1)}function l(a){if(le.open)if(a.key==="Tab"){const d=Array.from(n.value?.querySelectorAll('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])')??[]);if(d.length===0){a.preventDefault();return}const u=d[0],p=d[d.length-1];a.shiftKey&&document.activeElement===u?(a.preventDefault(),p.focus()):!a.shiftKey&&document.activeElement===p&&(a.preventDefault(),u.focus())}else a.key==="Escape"?(a.preventDefault(),i()):a.key==="Enter"&&(a.preventDefault(),o())}return bt(()=>le.open,a=>{if(a)s=document.activeElement instanceof HTMLElement?document.activeElement:null,window.addEventListener("keydown",l),Tn(()=>t.value?.focus());else{window.removeEventListener("keydown",l);const d=s;s=null,Tn(()=>d?.isConnected&&d.focus())}}),Nr(()=>window.removeEventListener("keydown",l)),(a,d)=>Ce(le).open?(M(),D("div",{key:0,class:"pk-overlay",onClick:zn(i,["self"])},[h("div",{ref_key:"modalRef",ref:n,class:Ie(["pk-modal",{danger:Ce(le).danger}]),role:"alertdialog","aria-modal":"true","aria-labelledby":"pk-modal-title"},[h("h3",du,H(Ce(le).title),1),(M(!0),D(se,null,ut(r.value,(u,p)=>(M(),D("p",{key:p,class:"pk-message"},H(u),1))),128)),h("div",pu,[h("button",{type:"button",class:"pk-btn cancel",onClick:i},H(Ce(le).cancelLabel),1),h("button",{ref_key:"confirmBtn",ref:t,type:"button",class:Ie(["pk-btn confirm",{danger:Ce(le).danger}]),onClick:o},H(Ce(le).confirmLabel),3)])],2)])):be("",!0)}}),[["__scopeId","data-v-3a559676"]]),gu={class:"tabs"},mu={key:0,class:"empty"},vu=ct({__name:"App",props:{ctx:{}},setup(e){const t=e;function n(l){const a=(l??"").replace(/^\/+|\/+$/g,"");return a==="synced"||a.startsWith("synced/")?"synced":"stores"}const s=ye(()=>n(t.ctx?.subPath));bt(()=>t.ctx?.basePath,l=>void 0,{immediate:!0}),bt(()=>t.ctx?.token,l=>Aa(l),{immediate:!0}),bt(()=>t.ctx?.tenant,l=>Ra(l),{immediate:!0});const r=ye(()=>!!t.ctx?.tenant),o=B(null);function i(l){const a=o.value;a&&a.dispatchEvent(new CustomEvent("faros-navigate",{detail:{path:l},bubbles:!0}))}return(l,a)=>(M(),D("div",{ref_key:"rootRef",ref:o,class:"app"},[h("nav",gu,[h("button",{class:Ie({active:s.value==="stores"}),onClick:a[0]||(a[0]=d=>i("stores"))},"Stores",2),h("button",{class:Ie({active:s.value==="synced"}),onClick:a[1]||(a[1]=d=>i("synced"))},"Synced Secrets",2)]),r.value?(M(),D(se,{key:1},[s.value==="synced"?(M(),xt(fu,{key:0})):(M(),xt(Mc,{key:1}))],64)):(M(),D("p",mu,"Select a workspace to manage secrets.")),X(hu)],512))}});class bu extends HTMLElement{_vueApp=null;_state=Bt({ctx:null});_host=null;set farosContext(t){this._state.ctx=t}get farosContext(){return this._state.ctx}connectedCallback(){this._vueApp||(this._host=document.createElement("div"),this._host.className="secrets-host",this.appendChild(this._host),this._vueApp=Ta({render:()=>jn(vu,{ctx:this._state.ctx})}),this._vueApp.mount(this._host))}disconnectedCallback(){this._vueApp&&(this._vueApp.unmount(),this._vueApp=null),this._host&&this._host.parentNode===this&&this.removeChild(this._host),this._host=null}}const yu=`/*
 * secrets provider element styles. Imported as a string by main.ts and injected
 * as one <style> tag in the host document. The element renders in LIGHT DOM so
 * the portal's CSS custom properties (--color-*) cascade in; every selector is
 * namespaced under faros-provider-secrets so these styles cannot leak into the
 * portal. Structure + tokens mirror the code provider so the two look like one
 * product.
 */

faros-provider-secrets {
  display: block;
  font-family: inherit;
  color: var(--color-text-primary, inherit);
}

faros-provider-secrets .app {
  padding: 8px 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* Tabs (segmented sub-nav) */
faros-provider-secrets .tabs {
  display: flex;
  gap: 4px;
}
faros-provider-secrets .tabs button {
  background: transparent;
  border: 1px solid transparent;
  border-radius: 4px;
  padding: 5px 12px;
  cursor: pointer;
  color: var(--color-text-secondary, #8a8ca6);
  font: inherit;
  font-size: 12px;
  font-weight: 500;
}
faros-provider-secrets .tabs button:hover {
  color: var(--color-text-primary, inherit);
  background: var(--color-surface-hover, rgba(255, 255, 255, 0.07));
}
faros-provider-secrets .tabs button.active {
  color: var(--color-accent, #8b6bff);
  background: var(--color-accent-subtle, rgba(139, 107, 255, 0.14));
  border-color: var(--color-accent, #8b6bff);
  box-shadow: 0 0 14px var(--color-accent-glow);
}

/* Page scaffold */
faros-provider-secrets .page {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
faros-provider-secrets .page-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
faros-provider-secrets .page-title {
  margin: 0 0 4px;
  font-size: 15px;
  font-weight: 600;
}
faros-provider-secrets .page-meta {
  margin: 0;
  font-size: 12px;
  color: var(--color-text-secondary, #8a8ca6);
  max-width: 70ch;
  line-height: 1.5;
}

/* Panels (cards) */
faros-provider-secrets .panel {
  background: var(--color-surface-raised, #111320);
  border: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.07));
  border-radius: 6px;
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
faros-provider-secrets .panel-title {
  margin: 0;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  color: var(--color-text-secondary, currentColor);
}
faros-provider-secrets .panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

/* Buttons */
faros-provider-secrets button.primary {
  background: var(--color-accent, #8b6bff);
  color: #fff;
  border: 1px solid var(--color-accent, #8b6bff);
  border-radius: 4px;
  padding: 6px 14px;
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  box-shadow: 0 0 16px var(--color-accent-glow);
  transition: background-color 0.12s ease, box-shadow 0.12s ease;
}
faros-provider-secrets button.primary:hover {
  background: var(--color-accent-hover, #a18aff);
  box-shadow: 0 0 22px var(--color-accent-glow);
}
faros-provider-secrets button.primary:disabled {
  opacity: 0.55;
  cursor: progress;
}
faros-provider-secrets button.secondary {
  background: var(--color-surface-overlay, #171927);
  color: var(--color-text-primary, inherit);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
  border-radius: 4px;
  padding: 6px 14px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
faros-provider-secrets button.secondary:hover {
  background: var(--color-surface-hover, #1e2033);
}
faros-provider-secrets button.danger {
  background: transparent;
  color: var(--color-danger, #ff5d5d);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
  border-radius: 4px;
  padding: 5px 12px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s ease;
}
faros-provider-secrets button.danger:hover {
  border-color: var(--color-danger, #ff5d5d);
  background: var(--color-danger-subtle, rgba(255, 93, 93, 0.12));
}
faros-provider-secrets button.danger:disabled {
  opacity: 0.4;
  cursor: default;
}
faros-provider-secrets button.link {
  background: transparent;
  border: none;
  color: var(--color-accent, #8b6bff);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
  padding: 0;
}
faros-provider-secrets button.link:hover {
  color: var(--color-accent-hover, #a18aff);
}

faros-provider-secrets .actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* Forms */
faros-provider-secrets .form {
  display: flex;
  flex-direction: column;
  gap: 12px;
  max-width: 36rem;
}
faros-provider-secrets .field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
faros-provider-secrets .field-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-secondary, currentColor);
}
faros-provider-secrets .field input,
faros-provider-secrets .field select,
faros-provider-secrets .field textarea {
  background: var(--color-surface-overlay, #171927);
  color: var(--color-text-primary, inherit);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
  border-radius: 4px;
  padding: 6px 10px;
  font: inherit;
  font-size: 12px;
  width: 100%;
  box-sizing: border-box;
}
faros-provider-secrets .field input::placeholder,
faros-provider-secrets .field textarea::placeholder {
  color: var(--color-text-muted, #5d5f78);
}
faros-provider-secrets .field input:focus,
faros-provider-secrets .field select:focus,
faros-provider-secrets .field textarea:focus {
  outline: none;
  border-color: var(--color-accent, #8b6bff);
  box-shadow: 0 0 0 3px var(--color-accent-subtle), 0 0 14px var(--color-accent-glow);
}
faros-provider-secrets .field .muted {
  margin: 0;
}

/* Repeated rows inside a form (dataFrom paths / key mappings) */
faros-provider-secrets .row-line {
  display: flex;
  gap: 8px;
  align-items: center;
}
faros-provider-secrets .row-line input {
  flex: 1;
  min-width: 0;
}

/* Mono identifiers (names, addresses, hashes) */
faros-provider-secrets .mono {
  font-family: var(--font-mono, "IBM Plex Mono", ui-monospace, Menlo, monospace);
  font-size: 12px;
}

/* Solid destructive button — emphasis variant for confirm-dialog actions */
faros-provider-secrets button.danger-solid {
  background: var(--color-danger, #ff5d5d);
  color: #fff;
  border: 1px solid var(--color-danger, #ff5d5d);
  border-radius: 4px;
  padding: 6px 14px;
  font: inherit;
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: background-color 0.12s ease;
}
faros-provider-secrets button.danger-solid:hover {
  background: var(--color-danger-hover, #e64c4c);
}

/* Confirm dialog (in-app modal replacing window.confirm) */
faros-provider-secrets .modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: rgba(0, 0, 0, 0.55);
  backdrop-filter: blur(2px);
}
faros-provider-secrets .modal {
  width: 100%;
  max-width: 26rem;
  background: var(--color-surface-raised, #111320);
  border: 1px solid var(--color-border-default, rgba(255, 255, 255, 0.11));
  border-radius: 6px;
  padding: 18px 20px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
}
faros-provider-secrets .modal-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary, inherit);
}
faros-provider-secrets .modal-message {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--color-text-secondary, #8a8ca6);
}
faros-provider-secrets .modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 6px;
}

/* Misc text */
faros-provider-secrets .muted {
  color: var(--color-text-muted, #5d5f78);
  font-size: 12px;
}
faros-provider-secrets .error {
  color: var(--color-danger, #ff5d5d);
  font-size: 12px;
}
faros-provider-secrets .empty {
  color: var(--color-text-secondary, #8a8ca6);
  font-size: 12px;
  padding: 8px 0;
}
faros-provider-secrets a {
  color: var(--color-accent, #8b6bff);
  text-decoration: none;
}
faros-provider-secrets a:hover {
  text-decoration: underline;
}
faros-provider-secrets code {
  background: var(--color-surface-overlay, rgba(0, 0, 0, 0.04));
  color: var(--color-text-secondary, currentColor);
  padding: 1px 5px;
  border-radius: 4px;
  font-family: "IBM Plex Mono", ui-monospace, Menlo, monospace;
  font-size: 11px;
}

/* PortalKit ResourceTable */
faros-provider-secrets .resource-table {
  background: color-mix(in srgb, var(--color-surface-raised, #111320) 80%, transparent);
  border: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.07));
  border-radius: 6px;
  overflow: hidden;
}

faros-provider-secrets .resource-table-error {
  align-items: center;
  color: var(--color-danger, #ff5d5d);
  display: flex;
  font-size: 13px;
  gap: 8px;
  padding: 16px;
}

faros-provider-secrets .resource-table-error-icon {
  flex-shrink: 0;
  height: 16px;
  width: 16px;
}

faros-provider-secrets .resource-table-stale {
  align-items: center;
  border-bottom: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.07));
  color: var(--color-danger, #ff5d5d);
  display: flex;
  font-size: 12px;
  gap: 8px;
  padding: 10px 16px;
}

faros-provider-secrets .resource-table-retry {
  background: transparent;
  border: 1px solid color-mix(in srgb, currentColor 35%, transparent);
  border-radius: 4px;
  color: inherit;
  cursor: pointer;
  font: inherit;
  font-size: 11px;
  margin-left: auto;
  padding: 3px 10px;
}

faros-provider-secrets .resource-table-loading-head,
faros-provider-secrets .resource-table-loading-row {
  border-bottom: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.07));
}

faros-provider-secrets .resource-table-loading-head {
  padding: 12px 20px;
}

faros-provider-secrets .resource-table-loading-row {
  align-items: center;
  display: flex;
  gap: 24px;
  padding: 14px 20px;
}

faros-provider-secrets .resource-table-loading-row:last-child {
  border-bottom: none;
}

faros-provider-secrets .resource-table-skeleton {
  border-radius: 4px;
  height: 12px;
}

faros-provider-secrets .resource-table-skeleton-short {
  width: 96px;
}

faros-provider-secrets .resource-table-skeleton-wide {
  width: 128px;
}

faros-provider-secrets .resource-table-skeleton-mid {
  width: 80px;
}

faros-provider-secrets .resource-table-skeleton-small {
  width: 64px;
}

faros-provider-secrets .shimmer {
  background: linear-gradient(90deg, var(--color-surface-overlay, #171927), var(--color-surface-hover, #1e2033), var(--color-surface-overlay, #171927));
  background-size: 200% 100%;
  animation: provider-shimmer 1.4s ease-in-out infinite;
}

faros-provider-secrets .resource-table-table {
  border-collapse: collapse;
  min-width: 100%;
  width: 100%;
}

faros-provider-secrets .resource-table-head-row,
faros-provider-secrets .resource-table-row {
  border-bottom: 1px solid var(--color-border-subtle, rgba(255, 255, 255, 0.07));
}

faros-provider-secrets .resource-table-row {
  transition: background-color 0.15s ease, color 0.1s ease;
}

faros-provider-secrets .resource-table-row:last-child {
  border-bottom: none;
}

faros-provider-secrets .resource-table-row.is-interactive {
  cursor: pointer;
}

faros-provider-secrets .resource-table-row.is-interactive:hover {
  background: color-mix(in srgb, var(--color-accent, #8b6bff) 3%, transparent);
}

faros-provider-secrets .resource-table-heading {
  color: var(--color-text-muted, #5d5f78);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.15em;
  padding: 12px 20px;
  text-align: left;
  text-transform: uppercase;
}

faros-provider-secrets .resource-table-cell {
  color: var(--color-text-secondary, #8a8ca6);
  font-size: 13px;
  padding: 12px 20px;
  transition: color 0.1s ease;
  white-space: nowrap;
}

faros-provider-secrets .resource-table-row.is-interactive:hover .resource-table-cell {
  color: var(--color-text-primary, inherit);
}

faros-provider-secrets .resource-table-empty-cell {
  padding: 64px 20px;
  text-align: center;
}

faros-provider-secrets .resource-table-empty-icon {
  color: color-mix(in srgb, var(--color-text-muted, #5d5f78) 20%, transparent);
  height: 32px;
  margin: 0 auto;
  width: 32px;
}

faros-provider-secrets .resource-table-empty-label {
  color: var(--color-text-muted, #5d5f78);
  font-size: 12px;
  margin: 8px 0 0;
}

/* PortalKit StatusBadge (square mono tags) */
faros-provider-secrets .status-badge {
  align-items: center;
  border: 1px solid color-mix(in srgb, currentColor 35%, transparent);
  border-radius: 3px;
  display: inline-flex;
  font-family: var(--font-mono, "IBM Plex Mono", ui-monospace, Menlo, monospace);
  font-size: 10px;
  font-weight: 600;
  gap: 6px;
  letter-spacing: 0.06em;
  padding: 2.5px 8px;
  text-transform: uppercase;
  transition: background-color 0.15s ease, color 0.15s ease;
}

faros-provider-secrets .status-badge.tone-success {
  background: var(--color-success-subtle, rgba(47, 214, 160, 0.12));
  color: var(--color-success, #2fd6a0);
}

faros-provider-secrets .status-badge.tone-warning {
  background: var(--color-warning-subtle, rgba(240, 166, 58, 0.12));
  color: var(--color-warning, #f0a63a);
}

faros-provider-secrets .status-badge.tone-danger {
  background: var(--color-danger-subtle, rgba(255, 93, 93, 0.12));
  color: var(--color-danger, #ff5d5d);
}

faros-provider-secrets .status-badge.tone-muted {
  background: var(--color-surface-overlay, #171927);
  color: var(--color-text-muted, #5d5f78);
}

faros-provider-secrets .status-badge-dot-wrap {
  display: flex;
  height: 6px;
  position: relative;
  width: 6px;
}

faros-provider-secrets .status-badge-pulse,
faros-provider-secrets .status-badge-dot {
  border-radius: 999px;
  display: inline-flex;
  height: 6px;
  position: absolute;
  width: 6px;
}

faros-provider-secrets .status-badge-pulse {
  animation: provider-live-ping 1.4s cubic-bezier(0, 0, 0.2, 1) infinite;
  opacity: 0.6;
}

faros-provider-secrets .dot-success,
faros-provider-secrets .pulse-success {
  background: var(--color-success, #2fd6a0);
}

faros-provider-secrets .dot-warning,
faros-provider-secrets .pulse-warning {
  background: var(--color-warning, #f0a63a);
}

faros-provider-secrets .dot-danger,
faros-provider-secrets .pulse-danger {
  background: var(--color-danger, #ff5d5d);
}

faros-provider-secrets .dot-muted,
faros-provider-secrets .pulse-muted {
  background: var(--color-text-muted, #5d5f78);
}

/* PortalKit ConditionsPanel */
faros-provider-secrets .conditions-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

faros-provider-secrets .conditions-title {
  color: var(--color-text-secondary, #8a8ca6);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.04em;
  margin: 0;
  text-transform: uppercase;
}

faros-provider-secrets .conditions-stale {
  color: var(--color-warning, #f0a63a);
  font-size: 12px;
  margin: 0;
}

faros-provider-secrets .conditions-type {
  color: var(--color-text-primary, inherit);
  font-weight: 600;
}

faros-provider-secrets .conditions-message {
  display: block;
  max-width: 40ch;
  overflow-wrap: anywhere;
  white-space: normal;
}

faros-provider-secrets .conditions-muted {
  color: var(--color-text-muted, #5d5f78);
}

@keyframes provider-shimmer {
  0% {
    background-position: 200% 0;
  }
  100% {
    background-position: -200% 0;
  }
}

@keyframes provider-live-ping {
  75%,
  100% {
    opacity: 0;
    transform: scale(2);
  }
}
`,Us="faros-provider-secrets";if(!customElements.get(Us)){const e=`${Us}-css`;if(!document.getElementById(e)){const t=document.createElement("style");t.id=e,t.textContent=yu,document.head.appendChild(t)}customElements.define(Us,bu)}})();
