/* Design simulator. No WebAuthn, slots, daemon, storage or live URLs. */
const $ = (s) => document.querySelector(s);
function el(tag, text, cls) { const n = document.createElement(tag); if (text != null) n.textContent = text; if (cls) n.className = cls; return n; }
function box(cls, ...children) { const n = el('div', null, cls); n.append(...children.filter(Boolean)); return n; }
function button(text, fn, cls = '') { const n = el('button', text, cls); n.onclick = fn; return n; }
function para(text) { return el('p', text); }
function heading(text) { return el('h1', text); }
function note(title, text) { return box('handoff', el('strong', title), para(text)); }
function details(title, text) { const n = el('details'); n.append(el('summary', title), para(text)); return n; }
const journeys = {
 create: ['Prepare', 'Save passkey', 'Passkey sent', 'Add this machine', 'Approval sent', 'Finishing', 'Created'],
 join: ['Prepare', 'Authorize machine', 'Approval sent', 'Reading directory', 'Check fingerprint', 'Matched', 'Mismatch'],
 add: ['Instructions', 'New member observed'],
 revoke: ['Confirm removal', 'Authorize removal', 'Approval sent', 'Notifying peers', 'Revoked here'],
 reset: ['Confirm reset', 'Reset complete'],
 recovery: ['Wrong passkey', 'PRF unsupported', 'Cancelled', 'Expired', 'Delivery unknown', 'Already answered', 'Disconnected', 'Directory unavailable', 'Browser unsupported', 'PRF unknown', 'PRF reported false', 'Invalid link', 'Homepage']
};
const q = new URLSearchParams(location.search);
let flow = Object.hasOwn(journeys, q.get('flow')) ? q.get('flow') : 'create';
let step = Math.min(Math.max(Number(q.get('step')) || 0, 0), journeys[flow].length - 1);
let machine = 'laptop', fleet = 'Personal machines', minimized = false;
let theme = q.get('theme') === 'dark' ? 'dark' : 'light';
const view = location.pathname.endsWith('browser.html') ? 'browser' : location.pathname.endsWith('desktop.html') ? 'desktop' : 'paired';
const fp = 'b7f3 9a21 0c4e 55d1', fleetFp = '3f9a 0c4e 7d12 e805';
function go(i) { step = i; minimized = false; render(); }
function choose(f) { flow = f; step = 0; minimized = false; render(); }
function identity(op) {
 const n = el('dl', null, 'identity');
 const name = op === 'r' ? 'oldlaptop' : machine;
 const action = op === 'c' ? `Create fleet passkey for “${fleet}”` : op === 'r' ? `Remove “${name}” from fleet` : `Add “${name}” to fleet`;
 for (const [k,v] of [['Action',action],['Machine',name],['Machine fingerprint',op === 'r' ? 'c80e 105b 3af9 2210' : fp]]) {
  const row = el('div'); const value=el('dd'); value.append(el(k === 'Machine fingerprint' ? 'code' : 'span',v)); row.append(el('dt',k),value); n.append(row);
 } return n;
}
function rail(second) { return box('rail',box(second ? '' : 'active',el('span','1','number'),el('span','Save a passkey')),box(second ? 'active' : '',el('span','2','number'),el('span','Add this machine'))); }
function field(label, value, fn) { const l=el('label',label), i=el('input'); i.value=value; i.oninput=()=>fn(i.value); l.append(i); return l; }
function form(create) {
 const n=box('stack',heading(create ? 'Create a fleet' : 'Join a fleet'),para(create ? 'Save one passkey for your fleet, then use it to add this machine. Expect two passkey prompts.' : 'Use the passkey for your existing fleet. This takes one passkey prompt.'),field('Machine name',machine,v=>machine=v));
 if(create) n.append(field('Fleet name',fleet,v=>fleet=v),el('small','Shown in your passkey manager.'));
 const error=el('p',null,'danger-text');
 n.append(error,button('Continue',()=>{ const valid=s=>[...s].length>0&&[...s].length<=64&&!/[\/\\{}\u0000-\u001f\u007f-\u009f]/.test(s); machine=machine||'laptop'; fleet=fleet||'beam'; if(!valid(machine)||(create&&!valid(fleet))) error.textContent='Use 1–64 characters, without slashes, braces or control characters.'; else go(1); },'primary'));
 return n;
}
function desktopPrompt(op) {
 const second=flow==='create'&&step>=3;
 return box('stack',flow==='create'?rail(second):null,heading(op==='c'?'Save a passkey':op==='r'?'Approve removal':'Add this machine'),note('Next · browser or phone',op==='c'?'Save the passkey there. Come back here for the second link.':'Use your fleet’s passkey there. Come back here to see the result.'),identity(op),box('qr',box('qr-box',el('span','QR placement\nDesign only')),box('stack',button('Open in browser',()=>{if(view==='desktop')location.href='browser.html?'+new URLSearchParams({flow,step:String(step),theme});else document.querySelector('#browser')?.scrollIntoView({behavior:'smooth'});},'primary'),el('small','Or scan this step’s QR with your phone.'))),para('Check the action, machine name and machine fingerprint on the page.'),el('small','Keep this link private. Requests expire after five minutes.'),box('actions',button('Copy link',e=>{e.currentTarget.textContent='Demo · no live link';}),button('Cancel request',()=>{flow='recovery';go(2);}),button('Keep working',()=>{minimized=true;render();})));
}
function browserPrompt(op) {
 const title=op==='c'?'Create your fleet passkey':op==='r'?'Remove oldlaptop from your fleet':`Authorize ${machine}`;
 const context=op==='c'?'Step 1 of 2':op==='r'?'Remove a machine':'Add a machine';
 return box('stack',el('div',context,'eyebrow'),heading(title),para(op==='c'?'Save a passkey, then use it to add this machine in a second prompt.':op==='r'?'This permanently revokes this machine identity. Offline machines learn when they reconnect.':'Use your fleet’s passkey. If you just created it, this is step 2 of 2.'),identity(op),note('Check before continuing','Continue only if you started this request just now. Compare the action, machine name and machine fingerprint with the terminal or app where you started.'),button(op==='c'?'Create passkey':op==='r'?'Authorize removal':'Authorize machine',()=>go(step+1),op==='r'?'danger':'primary'),box('actions',button('Cancel request',()=>{flow='recovery';go(2);}),button('Copy link',e=>{e.currentTarget.textContent='Demo · no live link';})),el('small','Requests expire after five minutes. Check the originating machine to see whether this request is still active.'),details('Passkey help','beam needs WebAuthn PRF from your browser, operating system and passkey provider, plus X25519 in Web Crypto. Browser support alone does not prove that your passkey works. The production page retains the documented compatibility table, including unverified combinations.'));
}
function sent(op) {
 const request=el('details');request.append(el('summary','Request details'),identity(op));
 return box('stack',el('div','Browser step finished','eyebrow'),heading(op==='c'?'Passkey created. One more step.':op==='r'?'Revocation approval sent.':'Approval sent.'),note('Return to the machine where you started',op==='c'?'Open the new link or scan the new QR code from your terminal or app. Use the same passkey to add this machine.':op==='r'?'Check that removal completed there. You can also see publication and acknowledgement status.':`Check whether ${machine} joined. Then compare its fleet fingerprint with a machine already in your fleet.`),para('This page cannot confirm the result on your machine. You can close this tab.'),request,details('Technical details','Passkey result: ok\nDelivery: HTTP 201\nDaemon verification: not visible to this page'));
}
function home() {
 const join=el('details');join.append(el('summary','Joining an existing fleet?'),para('Run this instead, using your existing fleet passkey:'),box('command',el('code','beam join')));
 return box('stack',heading('Connect this machine'),para('Start in the terminal on the machine you want to connect.'),box('command',el('small','Create a fleet'),el('code','beam init')),para('Open the link beam gives you, or scan its QR code with your phone. You’ll use your passkey here.'),join,details('Using an app with beam?','Start the create or join request in that app, then open the link it gives you.'));
}
function addPanel() { return box('stack',el('h3','Add a machine'),note('Next · your other machine','Open n10 Desktop → Fleet → Join a fleet. Use your existing fleet passkey.'),para('Or run this on the other machine:'),el('code','beam join --label my-server'),para('Open its link or scan its QR code.'),box('identity',el('small','Compare its fleet fingerprint with this:'),el('code',fleetFp)),para('Waiting for a new machine…'),button('Close instructions',()=>{minimized=true;render();})); }
function resetPanel() { const n=box('stack',el('h3','Reset fleet here?'),para('Disconnect this machine and delete queued messages to its old peers. Other machines and your passkey stay unchanged.'),para('This keeps this machine’s identity. It will not recover a lost passkey or undo a revocation.')); const b=button('Reset fleet',()=>go(1),'danger'); b.disabled=true; n.append(field('Type reset to confirm','',v=>b.disabled=v!=='reset'),box('actions',b,button('Cancel',()=>choose('add')))); return n; }
function completed(title,copy) { return box('stack',el('div','✓','status-icon'),heading(title),para(copy),note('Saved here. Waiting to sync…','Directory publication is pending; beam will retry while it runs.')); }
function desktopContent() {
 if(flow==='create') { if(step===0)return form(true); if(step<5)return desktopPrompt(step<3?'c':'a'); if(step===5)return box('stack',rail(true),heading('Finishing setup…'),para('Publishing your membership. No more passkey prompts.')); return completed('Fleet created','This machine is in your fleet. Add another from Fleet +.'); }
 if(flow==='join') { if(step===0)return form(false); if(step<3)return desktopPrompt('a'); if(step===3)return box('stack',heading('Finding machines…'),para('Reading the fleet directory. Keep n10 Desktop open.')); if(step===4)return box('stack',heading('Check fleet fingerprint'),para('This machine joined. Before using remote connections, compare with a machine already in your fleet: Fleet … → Copy fleet fingerprint.'),box('identity',el('small','Fleet fingerprint · not the machine fingerprint'),el('code',fleetFp)),box('actions',button('Matches',()=>go(5),'primary'),button('Doesn’t match',()=>go(6)))); if(step===5)return completed('Fleet fingerprint checked','Your machines appear in Fleet. Connection status is shown separately.'); return box('stack',heading('Different fleet'),para('Stop using remote connections. Reset here, then join with your fleet’s passkey.'),button('Reset fleet…',()=>choose('reset'),'danger')); }
 if(flow==='revoke') { if(step===0)return box('stack',heading('Revoke access to oldlaptop?'),identity('r'),para('This permanently revokes this machine identity. Files and programs remain. Offline machines learn when they reconnect. Reset cannot restore revoked access.'),box('actions',button('Revoke access',()=>go(1),'danger'),button('Cancel',()=>choose('add')))); if(step<3)return desktopPrompt('r'); if(step===3)return box('stack',heading('Updating machines…'),para('Waiting for beam’s result.')); return completed('Access revoked here','Acknowledged by 2 peers. Offline peers learn when they reconnect.'); }
 if(flow==='recovery')return recovery(false);
 return null;
}
const failures = [
 ['Wrong fleet passkey','This passkey does not unlock the expected fleet. Choose the original fleet passkey.','wrong-passkey'],
 ['This passkey did not provide WebAuthn PRF.','The browser, operating system and provider must all support PRF. A passkey may have been saved, but fleet creation is not complete.','prf-unsupported'],
 ['Passkey request cancelled or not allowed.','You may have cancelled, the prompt may have expired, or no eligible passkey was available. Start a fresh request on the originating machine.','ceremony-cancelled'],
 ['This request expired','Start again to get a new link and QR code.','ceremony-timeout'],
 ['Could not confirm delivery.','Check the originating machine before starting again: it may have received your answer. If it is still waiting, cancel there and start a fresh request.','Delivery: no response'],
 ['This request was already answered.','Check the originating machine. If you did not approve that answer and it enrolled, reset the fleet there. A reset does not undo a completed revocation.','Delivery: HTTP 409'],
 ['Reconnecting… Status may be out of date.','The request may have completed. Check fleet status before retrying.','connection-lost'],
 ['Cannot read the fleet directory','Check the connection to beam.n10.is, then try joining again with a fresh request.','directory-unavailable'],
 ['This browser cannot complete the request','Use a browser with WebAuthn and X25519 in Web Crypto. Nothing was sent. Copy the link to another device you own, or cancel on the originating machine.','Preflight: encryption unavailable'],
 ['Ready to try your passkey','Browser PRF capability is unknown. The selected passkey must still return PRF.','Preflight: PRF unknown'],
 ['This browser reports no PRF support','Your provider may handle passkeys separately. You can try, but beam still requires a valid PRF result.','Preflight: PRF false'],
 ['This link is incomplete or invalid.','Return to the terminal or app where you started and request a fresh link.','Invalid request']
];
function recovery(browser) {
 if(step===12)return home();
 if(browser&&[0,3,6,7].includes(step))return sent('a');
 if(!browser&&[4,5,8,9,10,11].includes(step))return box('stack',heading('Waiting for your passkey'),para('The browser may have more information. Desktop has not received a final wait result.'),button('Cancel request',()=>go(2)));
 const [title,copy,code]=failures[step];
 const n=box('stack',el('div','!','status-icon'),heading(title),para(copy),details('Technical details',code));
 if(browser&&step===8)n.append(button('Copy link',e=>e.currentTarget.textContent='Demo · no live link'));
 if(browser&&[9,10].includes(step))n.append(button(step===10?'Try anyway':'Authorize machine',()=>{flow='join';go(step===10?1:2);},'primary'),button('Cancel request',()=>go(2)),button('Copy link',e=>e.currentTarget.textContent='Demo · no live link'));
 if(!browser)n.append(button(step===6?'Check fleet status':'Start a fresh request',()=>choose('join'),'primary'));
 return n;
}
function browserContent() {
 if(flow==='recovery')return recovery(true);
 if(flow==='create')return step===0?home():step===1?browserPrompt('c'):step===2?sent('c'):step===3?browserPrompt('a'):sent('a');
 if(flow==='join')return step===0?home():step===1?browserPrompt('a'):sent('a');
 if(flow==='revoke')return step===0?home():step===1?browserPrompt('r'):sent('r');
 return home();
}
function renderDesktop() {
 const section=box('surface'); section.id='desktop'; section.append(box('surface-label',el('span','N10 DESKTOP'),el('span',flow==='add'?'Existing machine':'Originating machine')));
 const sidebar=box('sidebar',box('repo',el('strong','n10 / beam'),box('worktree',el('span','⌁  guided-fleet-ux')),el('small','Workspace stays open')),box('section-title',el('span','⌄ Fleet'),button('+',()=>choose('add')),button('…',()=>{const d=details('Fleet actions',fleetFp);d.open=true;d.append(button('Reset fleet…',()=>choose('reset')));sidebar.append(d);})));
 const enrolled=['add','revoke','reset'].includes(flow)||(flow==='create'&&step===6)||(flow==='join'&&step>=4);
 if(enrolled&&!(flow==='add'&&step===0&&!minimized))for(const [name,status,id] of [[machine,'This machine',fp],['buildbox','Connected · direct','05d1 6e7b 8810 c93a'],['oldlaptop',flow==='revoke'&&step===4?'Revoked':'Offline','c80e 105b 3af9 2210']].slice(0,flow==='create'?1:3))sidebar.append(box('machine',box('',el('strong',name)),el('span',status,status.startsWith('Connected')?'online':'small'),el('code',id)));
 if(flow==='add'&&!minimized)sidebar.append(step===0?addPanel():note('my-server joined','New member observed. Compare fingerprints on the joining machine.'));
 if(flow==='reset')sidebar.replaceChildren(el('h3','Fleet'),step===0?resetPanel():box('stack',note('Fleet reset here','Create a new fleet or join an existing one.'),button('Create a fleet',()=>choose('create'),'primary'),button('Join a fleet',()=>choose('join'))));
 const content=desktopContent();
 const workspace=box('workspace',el('div','beam  /  workspace','tab'),el('small','TERMINAL'),el('code','$ git status'),para('On branch design/guided-fleet-ux'),para('Your workspace stays here while you connect your machines.'));
 const stage=box('desktop-stage',sidebar,workspace);
 const autoComplete=(flow==='create'&&step===6)||(flow==='join'&&step===5)||(flow==='revoke'&&step===4);
 if(content&&!minimized&&!autoComplete) {const d=box('dialog',content);d.setAttribute('role','dialog');d.setAttribute('aria-label','Fleet setup preview');stage.append(box('shade',d));}
 if(content&&(minimized||autoComplete))sidebar.append(autoComplete?content:box('stack',para('Passkey step in progress'),button('Resume setup',()=>{minimized=false;render();})));
 section.append(stage);return section;
}
function renderBrowser() {
 const s=box('surface');s.id='browser';
 s.append(box('surface-label',el('span','BROWSER / PHONE'),el('span',(['add','reset'].includes(flow)||(['create','join','revoke'].includes(flow)&&step===0)||(flow==='recovery'&&step===12))?'No active request':'Passkey flow')),box('browser-body',box('browser-content',el('div','beam','signature'),browserContent())));
 return s;
}
function render() {
 document.documentElement.classList.toggle('dark',theme === 'dark'); document.body.className=view==='paired'?'':'single '+view+'-only'; $('#journey').value=flow;$('#theme').value=theme;
 $('#states').replaceChildren(...journeys[flow].map((name,i)=>{const b=button(name,()=>go(i));if(i===step)b.setAttribute('aria-current','step');return b;}));
 $('#surfaces').replaceChildren(...(view==='browser'?[renderBrowser()]:view==='desktop'?[renderDesktop()]:[renderDesktop(),renderBrowser()]));
 $('#evidence').textContent='Review state: '+journeys[flow][step]+'. Transitions below simulate events; they are not product controls.';
 $('#next').disabled=step===journeys[flow].length-1;
 const url=new URL(location);url.search=new URLSearchParams({flow,step:String(step),theme});history.replaceState(null,'',url);
}
for(const f of Object.keys(journeys)){const o=el('option',f[0].toUpperCase()+f.slice(1));o.value=f;$('#journey').append(o);}
$('#journey').onchange=e=>choose(e.target.value);$('#theme').onchange=e=>{theme=e.target.value;render();};$('#next').onclick=()=>go(step+1);render();
