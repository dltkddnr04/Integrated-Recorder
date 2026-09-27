import{t as a,j as t,p as u,O as n,P as p}from"./index-BKmny9f9.js";import{T as d,b as c}from"./query-state-Dz73wvgM.js";/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const i=a("CircleDashed",[["path",{d:"M10.1 2.182a10 10 0 0 1 3.8 0",key:"5ilxe3"}],["path",{d:"M13.9 21.818a10 10 0 0 1-3.8 0",key:"11zvb9"}],["path",{d:"M17.609 3.721a10 10 0 0 1 2.69 2.7",key:"1iw5b2"}],["path",{d:"M2.182 13.9a10 10 0 0 1 0-3.8",key:"c0bmvh"}],["path",{d:"M20.279 17.609a10 10 0 0 1-2.7 2.69",key:"1ruxm7"}],["path",{d:"M21.818 10.1a10 10 0 0 1 0 3.8",key:"qkgqxc"}],["path",{d:"M3.721 6.391a10 10 0 0 1 2.7-2.69",key:"1mcia2"}],["path",{d:"M6.391 20.279a10 10 0 0 1-2.69-2.7",key:"1fvljs"}]]);/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const o=a("CircleX",[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m15 9-6 6",key:"1uzhvr"}],["path",{d:"m9 9 6 6",key:"z0biqf"}]]);/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const y=a("Radio",[["path",{d:"M4.9 19.1C1 15.2 1 8.8 4.9 4.9",key:"1vaf9d"}],["path",{d:"M7.8 16.2c-2.3-2.3-2.3-6.1 0-8.5",key:"u1ii0m"}],["circle",{cx:"12",cy:"12",r:"2",key:"1c9p78"}],["path",{d:"M16.2 7.8c2.3 2.3 2.3 6.1 0 8.5",key:"1j5fej"}],["path",{d:"M19.1 4.9C23 8.8 23 15.1 19.1 19",key:"10b0cb"}]]);/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const l=a("Square",[["rect",{width:"18",height:"18",x:"3",y:"3",rx:"2",key:"afitv7"}]]),h={recording:"green",stopped:"neutral",completed:"blue",interrupted:"amber",failed:"red",verified:"green",degraded:"amber",unknown:"neutral",verifying:"blue",queued:"neutral",running:"blue",ready:"green",unavailable:"amber",rejected:"red",disabled:"neutral"},k={recording:y,stopped:l,completed:n,interrupted:d,failed:o,verified:n,degraded:d,verifying:c,unknown:i,queued:i,running:c,ready:n,unavailable:d,rejected:o,disabled:l},m={recording:"녹화 중",stopped:"중지됨",completed:"완료",interrupted:"중단됨"};function f({state:r}){const e=(r==null?void 0:r.toLowerCase())??"unknown",s=k[e]??i;return t.jsxs(u,{tone:h[e]??"neutral",children:[t.jsx(s,{className:`h-3 w-3 ${e==="running"||e==="verifying"?"animate-spin":""}`}),m[e]??p(e)]})}export{o as C,y as R,f as S};
