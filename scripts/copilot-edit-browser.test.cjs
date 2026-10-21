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
 const posts=[],ctx={document:'prd',path:'prd.json',agent:'writer',baseRevision:'head',baseDigest:'digest',content:'{"title":"Before"}'};
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
 assert.equal(nodes.has('edit-resume'),false,'no edit Resume listener/control');assert.equal(nodes.has('edit-rerun'),false,'no edit Rerun listener/control');
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

test('document edit admission stays disabled for every active flow including failed, without flow query or request association', async () => {
 const {createEditPanel}=await loadPanel();const nodes=new Map();
 global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',add(){},replaceChildren(){}});return nodes.get(id)}};
 global.Option=class{};global.sessionStorage={getItem:()=>null,setItem(){},removeItem(){}};global.crypto||=require('node:crypto').webcrypto;
 let flow=null;const reads=[],posts=[];
 const api=async(route,body)=>{if(body){posts.push(body);return {id:'edit-1'}};reads.push(route);if(route==='/api/edits')return {slot:'',edits:[],documents:[{id:'prd',path:'prd.json'}]};return {document:'prd',baseRevision:'head',baseDigest:'digest',flowRun:'',content:'{"title":"Before"}'}};
 const panel=createEditPanel({api,action:cb=>cb(),selectionChanged(){},currentFlow:()=>flow});
 await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});
 nodes.get('edit-instruction').value='Change title';nodes.get('edit-kind').value='whole';
 for(const state of ['starting','running','review','paused','uncertain','ready','cancelling','action','approved-not-executed','failed']) {
  flow={id:'flow-1',state};
  await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});
  nodes.get('edit-confirm').checked=true;panel.controls(true,true);
  assert.equal(nodes.get('edit-request').disabled,true,state+' must fence document-only admission');
  assert.equal(nodes.get('edit-confirm').disabled,true);
  assert.match(nodes.get('edit-status').textContent,/document-only.*active flow/i);
  await nodes.get('edit-request').onclick();
  assert.equal(posts.length,0,'even a stale click must not admit during '+state);
 }
 assert.ok(reads.filter(route=>route.startsWith('/api/edit/context')).every(route=>route==='/api/edit/context?document=prd'));
 for(const state of ['complete','rejected','cancelled','denied','exhausted']) {
  flow={id:'finished',state};
  await panel.refresh({head:'head'},{boot:'boot',activeInstance:''});nodes.get('edit-confirm').checked=true;panel.controls(true,true);
  assert.equal(nodes.get('edit-request').disabled,false,state+' preserves existing terminal semantics');
 }
 await nodes.get('edit-request').onclick();
 assert.equal(posts.length,1);assert.equal(Object.hasOwn(posts[0],'flowRun'),false);
});

test('document admission inspects every flow even when terminal history is selected', async () => {
 const source=fs.readFileSync(path.join(root,'.github/extensions/northwind-review/flow.js'),'utf8');
 const {createFlowPanel}=await import('data:text/javascript;base64,'+Buffer.from(source).toString('base64'));
 const nodes=new Map();global.document={getElementById(id){if(!nodes.has(id))nodes.set(id,{value:'',checked:false,disabled:false,hidden:false,textContent:'',dataset:{},add(){},append(){},replaceChildren(){},querySelectorAll(){return []}});return nodes.get(id)}};
 global.Option=class{};
 const panel=createFlowPanel({api:async()=>{},action:cb=>cb(),selectionChanged(){},requesterRole:'GM'});
 const flow={id:'release',definition:{steps:[],limits:{agentRuns:1}},schema:{type:'object',properties:{}}};
 const done={id:'old',flow:'release',state:'complete',position:0};
 const failed={id:'active',flow:'release',state:'failed',position:0};
 const snapshot={head:'head',definition:{title:'Fixture',id:'fixture',digest:'digest'},flows:[flow],runs:[done,failed],documents:{},operatorAgentRuns:1};
 panel.update(snapshot,{agents:[],runs:[],proposals:[]});
 nodes.get('flow-history').value='old';nodes.get('flow-history').onchange();
 assert.equal(panel.current().id,'old');
 assert.equal(panel.activeFlow().id,'active','failed work remains an admission fence independent of history selection');
 panel.update({...snapshot,runs:[done,{...failed,state:'cancelled'}]},{agents:[],runs:[],proposals:[]});
 assert.equal(panel.activeFlow(),undefined);
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
