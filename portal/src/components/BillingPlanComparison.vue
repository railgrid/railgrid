<script setup lang="ts">
import { computed, ref } from 'vue'
import { ArrowLeft, ArrowRight, Check, ExternalLink, Loader2 } from 'lucide-vue-next'
import type { OrgBilling, OrgBillingLimit, OrgBillingPlan } from '@/stores/tenant'
import { planPrice, priceAvailable, priceInterval } from '@/lib/billing-flow'

const props = defineProps<{
  billing: OrgBilling
  plans: OrgBillingPlan[]
  orgName: string
  canManage: boolean
  busy: string | null
  awaitingActivation: boolean
  focusedMeter: string | null
  overridesKnown: boolean
  canCheckout: (plan: OrgBillingPlan) => boolean
  suggestionForPlan: (meter: string, plan: OrgBillingPlan) => boolean
}>()
const emit = defineEmits<{
  back: []
  checkout: [plan: string]
  manage: []
  reload: []
}>()
const heading = ref<HTMLElement | null>(null)
defineExpose({ focusHeading: () => {
  heading.value?.focus({ preventScroll: true })
  heading.value?.closest('main')?.scrollTo({ top: 0 })
} })

// Positioning and hierarchy follow https://railgrid.ai/pricing/. Commercial
// amounts and every capacity below come exclusively from the billing response.
const descriptions: Record<string, string> = {
  free: 'Connect your first systems and build with your team and agents.',
  team: 'For organizations that need more shared capacity than Free provides.',
  enterprise: 'For organizations that need governance, deployment assistance, and contracted support.',
}
const labels: Record<string, string> = {
  workspaces: 'Workspaces', request_cost_rpm: 'Weighted API request units / min',
  agents: 'Agents', edges: 'Connected edges', services: 'Services',
  projects: 'App Studio projects', repositories: 'Repositories', instances: 'Infrastructure instances',
}
const order = ['workspaces', 'agents', 'edges', 'projects', 'repositories', 'services', 'instances', 'request_cost_rpm']
const plans = computed(() => [...props.plans].sort((a, b) => {
  const rank = (name: string) => ({ free: 0, team: 1, enterprise: 2 })[name] ?? 3
  return rank(a.name) - rank(b.name)
}))
const hasSubscription = computed(() => !!props.billing.subscription?.status && !['canceled', 'incomplete_expired'].includes(props.billing.subscription.status))
const managed = computed(() => props.billing.managedBy === 'operator')
const meters = computed(() => [...new Set(plans.value.flatMap(plan => plan.limits.map(limit => limit.meter)))].sort((a, b) => {
  const rank = (meter: string) => order.includes(meter) ? order.indexOf(meter) : order.length
  return rank(a) - rank(b) || a.localeCompare(b)
}))
function label(meter: string): string {
  return labels[meter] || meter.replace(/[_-]+/g, ' ').replace(/^./, letter => letter.toUpperCase())
}
function quantity(value?: string): string {
  if (value === undefined || value === '') return 'Not specified'
  if (!/^\d+$/.test(value)) return value
  const number = Number(value)
  return Number.isSafeInteger(number) ? new Intl.NumberFormat().format(number) : value
}
function capacity(limit?: OrgBillingLimit): string {
  if (!limit) return 'Not specified'
  return limit.enforce === 'Meter' ? 'Metered' : quantity(limit.max)
}
function limits(plan: OrgBillingPlan): OrgBillingLimit[] {
  return [...plan.limits].sort((a, b) => meters.value.indexOf(a.meter) - meters.value.indexOf(b.meter))
}
function price(plan: OrgBillingPlan): string {
  if (priceAvailable(plan.price)) return planPrice(plan.price)
  if (plan.sellable) return 'Price unavailable'
  if (plan.name === 'free' && !plan.sellable) return 'No subscription'
  return plan.name === 'enterprise' && !plan.sellable ? 'Custom' : 'Price unavailable'
}
function checkoutPlan(plan: OrgBillingPlan): boolean {
  return plan.sellable || plan.name === 'team'
}
function action(plan: OrgBillingPlan): string {
  if (plan.current) return 'Current plan'
  if (!props.canManage) return 'Organization admin required'
  if (hasSubscription.value) return !managed.value && plan.name === 'free' ? 'Downgrade to Free' : 'Manage subscription'
  if (managed.value) return 'Assigned by administrator'
  if (checkoutPlan(plan)) return priceAvailable(plan.price) ? `Continue to Stripe · ${planPrice(plan.price)} ${priceInterval(plan.price)}` : 'Continue to Stripe'
  return plan.name === 'enterprise' ? 'Talk to the team' : 'Plan unavailable'
}
function choose(plan: OrgBillingPlan): void {
  if (plan.current || props.busy || props.awaitingActivation) return
  if (!props.canManage) return
  if (hasSubscription.value && props.billing.enabled) emit('manage')
  else if (!managed.value && checkoutPlan(plan) && props.canCheckout(plan)) emit('checkout', plan.name)
}
function disabled(plan: OrgBillingPlan): boolean {
  return plan.current || !!props.busy || props.awaitingActivation || !props.canManage ||
    (hasSubscription.value ? !props.billing.enabled : managed.value || !checkoutPlan(plan) || !props.canCheckout(plan))
}
</script>

<template>
  <section class="plan-chooser" aria-labelledby="upgrade-review-heading">
    <nav class="chooser-nav" aria-label="Plan review">
      <button type="button" class="k-btn k-btn--ghost k-back-action" @click="emit('back')"><ArrowLeft :size="14" :stroke-width="1.75" aria-hidden="true" />Back to Plan &amp; billing</button>
      <div class="chooser-links">
        <a href="https://railgrid.ai/pricing/#billing-questions" target="_blank" rel="noopener noreferrer">Pricing FAQ<ExternalLink class="h-3.5 w-3.5" aria-hidden="true" /><span class="sr-only"> (opens in a new tab)</span></a>
        <a href="https://railgrid.ai/pricing/#compare" target="_blank" rel="noopener noreferrer">Compare plans<ExternalLink class="h-3.5 w-3.5" aria-hidden="true" /><span class="sr-only"> (opens in a new tab)</span></a>
      </div>
    </nav>
    <header class="chooser-intro">
      <h1 id="upgrade-review-heading" ref="heading" tabindex="-1">Change plan for {{ orgName }}</h1>
    </header>
    <p v-if="focusedMeter" class="capacity-context" role="status">You’re reviewing {{ label(focusedMeter).toLowerCase() }}. <template v-if="billing.usage[focusedMeter] !== undefined">{{ quantity(billing.usage[focusedMeter]) }} currently in use.</template> Your saved work stays available when you return from checkout.</p>
    <p v-if="managed" class="chooser-notice">Your plan is assigned by a platform administrator. <template v-if="hasSubscription">You can still manage or cancel your existing subscription in Stripe; your assigned allowances remain controlled by the administrator.</template></p>
    <p v-else-if="!canManage" class="chooser-notice">Only organization administrators can change or cancel the subscription.</p>
    <p v-else-if="!billing.enabled" class="chooser-notice">Stripe checkout is not available on this hub.</p>
    <p v-else-if="hasSubscription" class="chooser-notice">Change or cancel your existing subscription in Stripe. To return to Free, cancel the paid subscription and review the effective date in Stripe.</p>
    <p v-if="billing.overrides?.length" class="chooser-notice">Custom allowances apply to this organization. The capacities below include your overrides, which remain in effect after a plan change.</p>
    <div class="pricing-options" :style="{ '--plan-count': Math.min(plans.length, 3) }" aria-label="Available plans">
      <article v-for="plan in plans" :key="plan.name" class="pricing-option" :class="{ 'pricing-option--team': plan.name === 'team' }" :aria-labelledby="`plan-title-${plan.name}`">
        <div class="plan-card-title"><h3 :id="`plan-title-${plan.name}`">{{ plan.displayName || plan.name }}</h3><span v-if="plan.current" class="k-badge k-badge--muted">Current plan</span></div>
        <p class="plan-positioning">{{ descriptions[plan.name] || 'Compare the shared capacity available for your organization.' }}</p>
        <div class="plan-terms"><p :class="{ 'plan-terms--unquoted': !priceAvailable(plan.price) }">{{ price(plan) }}</p><span v-if="priceAvailable(plan.price)">{{ priceInterval(plan.price) }} · per organization</span><span v-else-if="checkoutPlan(plan)">A current price is required before checkout.</span><span v-else-if="plan.name === 'enterprise'">Talk to us about your requirements.</span><span v-else-if="plan.name !== 'free'">Confirm pricing and terms with your administrator.</span></div>
        <a v-if="!plan.current && plan.name === 'enterprise' && !plan.sellable && canManage && billing.managedBy !== 'operator' && !awaitingActivation" class="plan-action k-btn k-btn--ghost" href="https://railgrid.ai/contact/" target="_blank" rel="noopener noreferrer">Talk to the team<ExternalLink class="h-4 w-4" aria-hidden="true" /><span class="sr-only"> (opens in a new tab)</span></a>
        <button v-else type="button" class="plan-action k-btn" :class="plan.name === 'team' && !plan.current ? 'k-btn--primary' : 'k-btn--ghost'" :disabled="disabled(plan)" :aria-describedby="`plan-note-${plan.name}`" @click="choose(plan)"><Loader2 v-if="busy === plan.name" class="h-4 w-4 animate-spin" aria-hidden="true" />{{ action(plan) }}<ArrowRight v-if="!plan.current" class="h-4 w-4" aria-hidden="true" /></button>
        <p :id="`plan-note-${plan.name}`" class="plan-action-note"><template v-if="!plan.current && focusedMeter && !suggestionForPlan(focusedMeter, plan)">Additional headroom for this allowance is not confirmed.</template><template v-else-if="priceAvailable(plan.price)">Recurring subscription. {{ plan.price?.taxBehavior === 'inclusive' ? 'Includes tax.' : 'Tax and final total are confirmed in Stripe.' }}</template><template v-else-if="plan.current">Your organization’s current plan.</template><template v-else-if="hasSubscription && !managed && plan.name === 'free'">Cancel your paid subscription in Stripe to return to Free. Review the effective date there.</template><template v-else-if="checkoutPlan(plan) && canManage && !managed">{{ billing.enabled ? 'Reload pricing before continuing to Stripe.' : 'Stripe checkout is unavailable on this hub.' }}</template><template v-else>Confirm all applicable terms before changing plans.</template></p>
        <button v-if="checkoutPlan(plan) && !priceAvailable(plan.price)" type="button" class="reload-pricing" :disabled="!!busy" @click="emit('reload')">Reload pricing</button>
        <div class="plan-included"><h4>Included for your organization</h4><ul><li v-for="limit in limits(plan)" :key="limit.meter"><Check class="h-3.5 w-3.5" aria-hidden="true" /><div><span>{{ capacity(limit) }} {{ limit.meter === 'projects' ? 'App Studio projects' : label(limit.meter).toLowerCase() }}</span><small v-if="plan.current && billing.usage[limit.meter] !== undefined">{{ quantity(billing.usage[limit.meter]) }} currently in use</small><small v-else-if="focusedMeter === limit.meter && billing.usage[limit.meter] !== undefined">{{ quantity(billing.usage[limit.meter]) }} currently in use · {{ suggestionForPlan(limit.meter, plan) ? 'Additional capacity available' : 'No confirmed additional headroom' }}</small><small v-if="limit.enforce === 'Soft'">Soft limit</small></div></li></ul><p v-if="!plan.limits.length">This provider has not specified plan allowances.</p></div>
      </article>
    </div>
    <p v-if="!overridesKnown" class="pricing-footnote">This hub does not report organization overrides. Other plans show catalog allowances; effective capacity must be confirmed before changing plans.</p>

  </section>
</template>

<style scoped>
.plan-chooser{min-width:0;color:var(--color-text-primary)}
.chooser-links{display:flex;align-items:center;flex-wrap:wrap;gap:20px;margin-left:auto}.chooser-nav{display:flex;align-items:center;justify-content:space-between;gap:16px}.chooser-links>a{display:inline-flex;align-items:center;gap:7px;font-size:12px;color:var(--color-text-secondary);text-underline-offset:4px}.chooser-links>a:hover{text-decoration:underline;color:var(--color-text-primary)}
.chooser-intro{text-align:center;padding:24px 16px 36px}.chooser-intro h1{font-size:22px;line-height:1.4;font-weight:500;letter-spacing:-.025em;overflow-wrap:anywhere;text-wrap:balance}
.capacity-context,.chooser-notice{font-size:13px;line-height:1.7;padding:14px 18px;background:var(--color-accent-subtle);border-radius:6px;margin-bottom:20px}.chooser-notice{background:var(--color-surface-overlay)}
.pricing-options{display:grid;grid-template-columns:repeat(var(--plan-count),minmax(0,1fr));border:1px solid var(--color-border-default);border-radius:6px;background:var(--color-surface-raised)}.pricing-option{display:flex;flex-direction:column;min-width:0;padding:28px 24px}.pricing-option+.pricing-option{border-left:1px solid var(--color-border-default)}.pricing-option--team{outline:1px solid var(--color-accent);outline-offset:-1px;border-radius:6px}.plan-card-title{display:flex;align-items:center;flex-wrap:wrap;gap:10px;min-height:30px}.plan-card-title h3{font-size:23px;font-weight:500;letter-spacing:-.025em;overflow-wrap:anywhere}.plan-positioning{font-size:13px;line-height:1.8;color:var(--color-text-secondary);margin-top:12px;min-height:72px}.plan-terms{min-height:124px;padding-top:22px}.plan-terms>p{font-size:42px;font-weight:500;line-height:1.15;letter-spacing:-.035em;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.plan-terms>p.plan-terms--unquoted{font-size:27px;letter-spacing:-.025em}.plan-terms>span{display:block;margin-top:8px;font-size:12px;line-height:1.6;color:var(--color-text-secondary)}.plan-action{width:100%;min-height:44px;justify-content:space-between;height:auto;white-space:normal;text-align:left;line-height:1.5;font-size:12px}.plan-action svg{flex-shrink:0}.plan-action-note{min-height:57px;padding-top:10px;font-size:11px;line-height:1.6;color:var(--color-text-secondary)}.reload-pricing{font-size:12px;text-align:left;color:var(--color-accent);text-decoration:underline;text-underline-offset:3px;margin:8px 0}
.plan-included{border-top:1px solid var(--color-border-default);padding-top:20px;margin-top:12px}.plan-included h4{font-size:12px;font-weight:400;color:var(--color-text-secondary)}.plan-included ul{list-style:none;padding:0;margin:18px 0 0;display:flex;flex-direction:column;gap:17px}.plan-included li{display:flex;gap:10px;align-items:flex-start;font-size:13px;line-height:1.6}.plan-included li>svg{flex-shrink:0;color:var(--color-accent);margin-top:4px}.plan-included small{display:block;font-size:11px;color:var(--color-text-secondary);margin-top:3px}.plan-included>p{font-size:13px;color:var(--color-text-secondary);margin-top:16px}
.pricing-footnote{font-size:12px;line-height:1.8;color:var(--color-text-secondary);margin:16px 0 0;max-width:90ch}
.plan-chooser :is(button,a,summary,textarea,[tabindex]):focus-visible{outline:2px solid var(--color-accent);outline-offset:4px}
@container(max-width:760px){.pricing-options{grid-template-columns:1fr}.pricing-option+.pricing-option{border-left:0;border-top:1px solid var(--color-border-default)}.plan-positioning{min-height:0;max-width:65ch}.plan-terms{min-height:0;padding:24px 0}.plan-action-note{min-height:0;margin-bottom:12px}.chooser-intro{padding:28px 0}.chooser-nav{align-items:flex-start;flex-wrap:wrap}.chooser-links>a{padding:8px}.pricing-option{padding:24px}}
</style>
