import{a as r}from"./auth-CYG0PYiC.js";function u(){return r.get("/api/settings")}function f(){return r.get("/api/proxy-config/keys")}function a(t){const i={};for(const n of(t||"").split(`
`)){const e=n.trim();if(!e||e.startsWith("#"))continue;const s=e.indexOf(":");s<0||(i[e.slice(0,s).trim()]=e.slice(s+1).trim())}return i}function g(t,i){return i.filter(n=>t[n.key]!==void 0&&t[n.key]!=="").map(n=>`${n.key}: ${t[n.key]}`).join(`
`)}function c(){return r.get("/api/user-settings")}function p(t){return r.put("/api/user-settings",t)}export{c as a,f as b,g as f,u as g,a as p,p as u};
