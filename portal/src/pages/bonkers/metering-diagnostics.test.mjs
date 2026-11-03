import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import ts from 'typescript'
import { runInNewContext } from 'node:vm'

const context = { exports: {} }
function readModule(file) {
 const source = readFileSync(new URL(file, import.meta.url), 'utf8')
 return ts.transpileModule(source, {compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.CommonJS}}).outputText
}
runInNewContext(readModule('./metering-model.ts'), context)
context.require = () => context.exports
runInNewContext(readModule('./metering-diagnostics.ts'), context)
const { planApplication, usageFreshness } = context.exports
const now = Date.parse('2026-11-02T19:00:00Z')
const plan = {metadata:{name:'free'},spec:{limits:[{meter:'workspaces',shape:'Level',enforce:'Hard',max:'1'}]}}
function entitlement() { return {metadata:{name:'org',generation:2},spec:{plan:'free'},status:{observedGeneration:2,effectiveLimits:structuredClone(plan.spec.limits),conditions:[{type:'Ready',status:'True',lastTransitionTime:'2020-01-01T00:00:00Z'}],usage:{workspaces:'0'},window:{end:new Date(now).toISOString()}}} }

test('application checks reconcile the assignment, generation, limits and readiness independently of quota exhaustion',()=>{
 const ent=entitlement()
 assert.equal(planApplication({name:'org'},ent,plan).state,'Applied')
 ent.status.conditions.push({type:'OverHardLimit',status:'True'})
 assert.equal(planApplication({name:'org'},ent,plan).state,'Applied','a quota breach is not an unapplied plan')
 assert.equal(planApplication({name:'org',plan:'small'},ent,plan).state,'Applying')
 ent.status.observedGeneration=1
 assert.equal(planApplication({name:'org'},ent,plan).state,'Applying')
 ent.status.observedGeneration=2;ent.status.effectiveLimits[0].max='10'
 assert.equal(planApplication({name:'org'},ent,plan).state,'Applying')
})

test('application never treats missing resources or missing readiness as healthy',()=>{
 assert.equal(planApplication({name:'org'},undefined,plan).state,'Awaiting metering')
 assert.equal(planApplication({name:'org'},entitlement(),undefined).state,'Plan unavailable')
 const ent=entitlement();ent.status.conditions=[]
 assert.equal(planApplication({name:'org'},ent,plan).state,'Unconfirmed')
 ent.status.conditions=[{type:'Ready',status:'False',message:'Reconciliation failed'}]
 assert.equal(planApplication({name:'org'},ent,plan).detail,'Reconciliation failed')
})

test('freshness uses reporting time, accepts measured zero, and ages at the stated five-minute threshold',()=>{
 const ent=entitlement()
 assert.equal(usageFreshness(ent,now).state,'Recent','old condition transition times do not imply old usage')
 assert.equal(usageFreshness(ent,now+300_000).state,'Recent')
 assert.equal(usageFreshness(ent,now+300_001).state,'Stale')
 assert.equal(usageFreshness(ent,now-120_000).state,'Unknown','future clocks are not healthy')
})

test('missing and malformed samples cannot become a recent or zero-usage result',()=>{
 assert.equal(usageFreshness(undefined,now).state,'Unavailable')
 for(const usage of [{}, {workspaces:null}, {workspaces:''}, {other:'0'}]) {
  const ent=entitlement();ent.status.usage=usage
  assert.equal(usageFreshness(ent,now).state,'Unavailable')
 }
 for(const end of [undefined, '', 'not-a-date']) {
  const ent=entitlement();ent.status.window.end=end
  assert.equal(usageFreshness(ent,now).state,'Unknown')
 }
})
