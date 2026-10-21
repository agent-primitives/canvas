const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const root = path.join(__dirname, '..');
const modulePath = path.join(root, '.github/extensions/northwind-review/edit.js');
const loadPanel = () => import('data:text/javascript;base64,' + Buffer.from(fs.readFileSync(modulePath, 'utf8')).toString('base64'));

test('edit selector preview refuses missing targets, escaped pointers and ambiguous unions without widening', async () => {
 const { editSection } = await loadPanel();
 assert.deepEqual(editSection('whole', '', '', '1'), undefined);
 assert.deepEqual(editSection('json-pointer', '/title', '', '1'), { kind:'json-pointer',pointer:'/title' });
 assert.throws(() => editSection('json-pointer', 'title', '', '1'));
 assert.throws(() => editSection('json-pointer', '/bad~2', '', '1'));
 assert.deepEqual(editSection('markdown-heading', '', 'Main\nChild', '2'), {kind:'markdown-heading',headingPath:['Main','Child'],occurrence:2});
 assert.throws(() => editSection('markdown-heading', '', '', '1'));
 assert.throws(() => editSection('markdown-heading', '', 'Main', '1.2'));
 assert.throws(() => editSection('unknown', '', '', '1'));
});

test('edit panel retains one exact request after lost replies and never posts during refresh', async () => {
 const { createEditPanel } = await loadPanel();
 const nodes = new Map();
 global.document = { getElementById(id) { if (!nodes.has(id)) nodes.set(id,{ value:'',checked:false,disabled:false,hidden:false,textContent:'',dataset:{}, add(){},replaceChildren(){},setAttribute(){} }); return nodes.get(id); } };
 global.Option = class { constructor(text,value){this.text=text;this.value=value;} };
 let saved = null;
 global.sessionStorage = { getItem:()=>saved,setItem(key,value){saved=value;},removeItem(){saved=null;} };
 global.crypto ||= require('node:crypto').webcrypto;
 const reads=[];
 let loseReply = true;
 const posts=[],ctx={document:'prd',path:'prd.json',agent:'writer',baseRevision:'head',baseDigest:'digest',content:'{"title":"Before"}',flowRun:''};
 const api=async(route,body)=> {
  if(body){ posts.push(structuredClone(body)); if(loseReply){loseReply=false;throw new Error('lost reply');} return {id:'edit-1',state:'queued',request:body}; }
  if(route==='/api/edits')return {edits:[],slot:'',documents:[{id:'prd',title:'PRD',path:'prd.json',agent:'writer'}]};
  if(route.startsWith('/api/edit/context')){reads.push(route);return ctx;}
  throw new Error(route);
 };
 let failure;
 const options={api,action:async cb=>{try{await cb()}catch(e){failure=e}},selectionChanged(){},currentFlow:()=>null,requesterRole:'GM'};
 let panel=createEditPanel(options);
 await panel.refresh({head:'head',proposals:[]},{boot:'boot',activeInstance:''});
 nodes.get('edit-instruction').value='Change title';nodes.get('edit-kind').value='json-pointer';nodes.get('edit-pointer').value='/title';nodes.get('edit-confirm').checked=true;
 await nodes.get('edit-request').onclick();
 assert.match(failure.message,/lost reply/);assert.equal(posts.length,1);
 await panel.refresh({head:'head',proposals:[]},{boot:'boot',activeInstance:''});
 assert.equal(posts.length,1,'GET refresh cannot resubmit');
 assert.equal(panel.blocked(),true,'unknown request fences old Continue even before slot is observed');
 assert.deepEqual(JSON.parse(saved).body,posts[0]);
 panel=createEditPanel(options);
 await panel.refresh({head:'head',proposals:[]},{boot:'boot',activeInstance:''});
 assert.equal(posts.length,1,'sessionStorage restoration must not repost');
 await nodes.get('edit-continue').onclick();
 assert.equal(posts.length,2);assert.deepEqual(posts[1],posts[0]);
 assert.equal(posts[0].section.pointer,'/title');assert(!Object.hasOwn(posts[0],'flowRun'));
 assert.deepEqual(reads,['/api/edit/context?document=prd','/api/edit/context?document=prd']);
 assert.equal(saved,null,'only confirmed continuation clears the saved exact request');
 // A different inspected instance cannot reuse the retained old-instance body.
 loseReply=true;
 nodes.get('edit-confirm').checked=true;
 await nodes.get('edit-request').onclick();
 const count=posts.length;
 assert.ok(saved,'the second lost reply retains a real pending request');
 await panel.refresh({head:'head',proposals:[]},{boot:'new-boot',activeInstance:'foreign'});
 assert.equal(nodes.get('edit-continue').disabled,true);
 await nodes.get('edit-continue').onclick();
 assert.equal(posts.length,count);
});

test('edit maintenance releases only proven unreserved refusals and retains exact unknown commands', async () => {
 const {createEditPanel}=await loadPanel();
 for(const command of ['reconcile','abandon']) for(const refused of [true,false]) {
  const nodes=new Map();global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
  global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
  const posts=[];const edit={id:'edit-1',state:'uncertain',request:{document:'prd',role:'GM'}};
  const api=async(route,body)=>{if(body){posts.push({route,body:structuredClone(body)});if(posts.length===1)throw Object.assign(new Error('synthetic command outcome'),{code:refused?'EDIT_REFUSED':'EDIT_UNCERTAIN',status:refused?409:500,uncertain:!refused});return edit};if(route==='/api/edits')return {slot:'edit-1',edits:[edit],documents:[{id:'prd',path:'prd.json'}]};return {document:'prd',baseRevision:'head',flowRun:'',content:'{"title":"Before"}'}};
  const panel=createEditPanel({api,action:async cb=>{try{await cb()}catch{}},selectionChanged(){}});
  await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});nodes.get('edit-command-confirm').checked=true;
  await nodes.get('edit-'+command).onclick();
  assert.equal(posts[0].route,'/api/edit/'+command);assert.equal(posts[0].body.confirmed,true);assert.equal(posts[0].body.expectedHead,'head');assert.equal(posts[0].body.role,'GM');assert.equal(posts[0].body.edit,'edit-1');assert.ok(posts[0].body.requestKey);
  assert.equal(nodes.get('edit-continue').hidden,refused);
  assert.equal(panel.blocked(),true,'the durable slot still fences competing actions');
  if(!refused){await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});assert.equal(posts.length,1);await nodes.get('edit-continue').onclick();assert.deepEqual(posts[1],posts[0]);assert.equal(nodes.get('edit-continue').hidden,true)}
 }
});

test('flow edit rerun maintenance releases only proven unreserved refusals and retains exact unknown commands', async () => {
 const {createEditPanel}=await loadPanel();
 for(const refused of [true,false]) {
  const nodes=new Map();global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
  global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
  const posts=[];const edit={id:'edit-1',state:'accepted',request:{document:'prd',role:'GM',flowRun:'flow-1'}};
  const api=async(route,body)=>{if(body){posts.push({route,body:structuredClone(body)});if(posts.length===1)throw Object.assign(new Error('synthetic command outcome'),{code:refused?'EDIT_REFUSED':'EDIT_UNCERTAIN',status:refused?409:500,uncertain:!refused});return {id:'flow-1'}};if(route==='/api/edits')return {slot:'',edits:[edit],documents:[{id:'prd',path:'prd.json'}]};return {document:'prd',baseRevision:'head',flowRun:'flow-1',content:'{"title":"Before"}'}};
  const panel=createEditPanel({api,action:async cb=>{try{await cb()}catch{}},selectionChanged(){},currentFlow:()=>({id:'flow-1',state:'edited'})});
  await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});nodes.get('edit-command-confirm').checked=true;
  await nodes.get('edit-rerun').onclick();assert.equal(panel.blocked(),!refused);
  if(!refused){await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});assert.equal(posts.length,1);await nodes.get('edit-continue').onclick();assert.deepEqual(posts[1],posts[0]);assert.equal(panel.blocked(),false)}
 }
});

test('flow edit binds the inspected active flow and preserves that exact association after a lost reply', async () => {
 const {createEditPanel}=await loadPanel();const nodes=new Map();
 global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
 global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
 let flow={id:'flow-1',state:'review'};const reads=[],posts=[];
 const api=async(route,body)=>{
  if(body){posts.push(structuredClone(body));if(posts.length===1)throw new Error('lost reply');return {id:'edit-1'}}
  reads.push(route);
  if(route==='/api/edits')return {slot:'',edits:[],documents:[{id:'prd',path:'prd.json'}]};
  return {document:'prd',baseRevision:'head',baseDigest:'digest',flowRun:new URLSearchParams(route.split('?')[1]).get('flowRun')||'',content:'{"title":"Before"}'};
 };
 const panel=createEditPanel({api,action:async cb=>{try{await cb()}catch{}},selectionChanged(){},currentFlow:()=>flow});
 assert.equal(await panel.refresh({head:'head'},{boot:'boot',activeInstance:''}),true);
 nodes.get('edit-instruction').value='Change title';nodes.get('edit-kind').value='whole';nodes.get('edit-confirm').checked=true;panel.controls(true,true);
 assert.equal(nodes.get('edit-request').disabled,false,'an inspected active flow can receive an explicitly associated edit');
 const firstKey=panel.key();
 await nodes.get('edit-request').onclick();
 assert.equal(posts.length,1);assert.equal(posts[0].flowRun,'flow-1');assert.equal(panel.blocked(),true);
 flow={id:'flow-2',state:'review'};
 assert.equal(await panel.refresh({head:'head'},{boot:'boot',activeInstance:''}),true);
 assert.notEqual(panel.key(),firstKey,'flow association changes revoke old inspection');
 assert.equal(nodes.get('edit-confirm').checked,false);
 assert.equal(posts.length,1,'refresh never submits against the newly selected flow');
 await nodes.get('edit-continue').onclick();
 assert.equal(posts.length,2);assert.deepEqual(posts[1],posts[0],'exact continuation keeps the original flow, key and payload');
 assert.deepEqual(reads.filter(route=>route.startsWith('/api/edit/context')),['/api/edit/context?document=prd&flowRun=flow-1','/api/edit/context?document=prd&flowRun=flow-2']);
});

test('terminal flows never acquire a new edit association and mismatched flow context cannot dispatch', async () => {
 const {createEditPanel}=await loadPanel();
 for(const state of ['complete','rejected','cancelled','denied','exhausted','review']) {
  const nodes=new Map();global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
  global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
  const reads=[],posts=[];
  const api=async(route,body)=>{if(body){posts.push(body);return {id:'edit-1'}};reads.push(route);if(route==='/api/edits')return {slot:'',edits:[],documents:[{id:'prd',path:'prd.json'}]};return {document:'prd',baseRevision:'head',baseDigest:'digest',flowRun:state==='review'?'wrong-flow':'',content:'{"title":"Before"}'}};
  const panel=createEditPanel({api,action:cb=>cb(),selectionChanged(){},currentFlow:()=>({id:'selected',state})});
  const refreshed=await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});
  nodes.get('edit-instruction').value='Change title';nodes.get('edit-kind').value='whole';nodes.get('edit-confirm').checked=true;panel.controls(true,true);
  if(state==='review') {
   assert.equal(refreshed,false,'a context for another flow is never adopted');
   assert.equal(nodes.get('edit-request').disabled,true);
   await nodes.get('edit-request').onclick();assert.equal(posts.length,0);
   assert.deepEqual(reads.filter(route=>route.startsWith('/api/edit/context')),['/api/edit/context?document=prd&flowRun=selected']);
  } else {
   assert.equal(refreshed,true);assert.equal(nodes.get('edit-request').disabled,false);
   await nodes.get('edit-request').onclick();assert.equal(posts.length,1);assert.equal(Object.hasOwn(posts[0],'flowRun'),false);
   assert.deepEqual(reads.filter(route=>route.startsWith('/api/edit/context')),['/api/edit/context?document=prd']);
  }
 }
});

test('abandoned edit native settlement is explicit read-back and never a producer retry', async () => {
 const {createEditPanel}=await loadPanel();const nodes=new Map();
 global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
 global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
 let edit={id:'edit-1',state:'abandoned',request:{document:'prd',role:'GM'}};const posts=[];
 const api=async(route,body)=>{if(body){posts.push({route,body});edit={...edit,nativeSettled:true};return edit};if(route==='/api/edits')return {slot:'',edits:[edit],documents:[{id:'prd',path:'prd.json'}]};return {document:'prd',baseRevision:'head',flowRun:'',content:'{"title":"Before"}'}};
 const panel=createEditPanel({api,action:async cb=>cb(),selectionChanged(){}});
 await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});panel.controls(true,true);nodes.get('edit-command-confirm').checked=true;nodes.get('edit-command-confirm').onchange();
 assert.equal(nodes.get('edit-reconcile').disabled,false);assert.equal(posts.length,0);
 await nodes.get('edit-reconcile').onclick();await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});
 assert.equal(posts.length,1);assert.equal(posts[0].route,'/api/edit/reconcile');assert.equal(panel.current().state,'abandoned');assert.equal(panel.current().nativeSettled,true);
 nodes.get('edit-command-confirm').checked=true;nodes.get('edit-command-confirm').onchange();assert.equal(nodes.get('edit-reconcile').disabled,true);
});
