import{c as a,j as e,B as d}from"./index-Bw44-qrc.js";/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const m=a("LoaderCircle",[["path",{d:"M21 12a9 9 0 1 1-6.219-8.56",key:"13zald"}]]);/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const i=a("RefreshCw",[["path",{d:"M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8",key:"v9h5vc"}],["path",{d:"M21 3v5h-5",key:"1q7to0"}],["path",{d:"M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16",key:"3uifl3"}],["path",{d:"M8 16H3v5",key:"1cv678"}]]);/**
 * @license lucide-react v0.468.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const l=a("TriangleAlert",[["path",{d:"m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3",key:"wmoenq"}],["path",{d:"M12 9v4",key:"juzpu7"}],["path",{d:"M12 17h.01",key:"p32p05"}]]);function x({eyebrow:t,title:s,description:r,actions:n}){return e.jsxs("div",{className:"mb-6 flex min-w-0 flex-col justify-between gap-4 sm:flex-row sm:items-end",children:[e.jsxs("div",{className:"min-w-0",children:[e.jsx("p",{className:"mb-1 text-[11px] font-semibold tracking-[.16em] text-primary",children:t??"Integrated Recorder"}),e.jsx("h1",{className:"min-w-0 truncate text-2xl font-semibold tracking-tight md:text-[28px]",children:s}),r&&e.jsx("p",{className:"mt-1.5 text-sm text-muted-foreground",children:r})]}),n&&e.jsx("div",{className:"flex shrink-0 flex-wrap items-center gap-2",children:n})]})}function o({label:t="데이터를 불러오는 중입니다"}){return e.jsxs("div",{className:"flex min-h-40 items-center justify-center gap-3 text-sm text-muted-foreground",children:[e.jsx(m,{className:"h-5 w-5 animate-spin text-primary"}),t]})}function h({message:t,retry:s}){return e.jsxs("div",{role:"alert",className:"flex min-h-40 flex-col items-center justify-center rounded-lg border border-dashed border-destructive/30 bg-destructive/[.03] p-6 text-center",children:[e.jsx(l,{className:"h-5 w-5 text-destructive"}),e.jsx("p",{className:"mt-2 text-sm font-medium",children:"데이터를 불러오지 못했습니다"}),e.jsx("p",{className:"mt-1 max-w-xl text-xs leading-5 text-muted-foreground",children:t}),s&&e.jsxs(d,{variant:"outline",size:"sm",className:"mt-4",onClick:s,children:[e.jsx(i,{className:"h-3.5 w-3.5"}),"다시 시도"]})]})}function u({title:t,description:s}){return e.jsxs("div",{className:"flex min-h-40 flex-col items-center justify-center rounded-lg border border-dashed border-border px-5 text-center",children:[e.jsx("p",{className:"text-sm font-medium",children:t}),s&&e.jsx("p",{className:"mt-1 max-w-md text-xs leading-5 text-muted-foreground",children:s})]})}export{h as E,o as L,x as P,i as R,l as T,u as a,m as b};
