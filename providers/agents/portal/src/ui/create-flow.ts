import { html, nothing, type TemplateResult } from 'lit'
import { icon, type IconName } from './icon'

export interface FirstRunStep {
  label: string
  description: string
}

export interface FirstRunOptions {
  icon: IconName
  title: string
  description: string
  primaryLabel: string
  primary: () => void
  secondaryLabel?: string
  secondary?: () => void
  steps: readonly FirstRunStep[]
  currentStep?: number
  journeyLabel?: string
}

export function firstRunGuide(options: FirstRunOptions): TemplateResult {
  const current = Math.max(0, Math.min(options.currentStep ?? 0, Math.max(0, options.steps.length - 1)))
  return html`<section class="k-first-run agents-state-empty" aria-label=${options.title}>
    <div class="k-first-run__lead">
      <span class="k-first-run__icon" aria-hidden="true">${icon(options.icon)}</span>
      <div class="k-first-run__copy">
        <h3>${options.title}</h3>
        <p>${options.description}</p>
      </div>
      <div class="k-first-run__actions">
        <button class="k-btn k-btn--primary" type="button" @click=${options.primary}>
          ${options.primaryLabel} ${icon('chevron-right')}
        </button>
        ${options.secondaryLabel && options.secondary
          ? html`<button class="k-btn k-btn--ghost" type="button" @click=${options.secondary}>${options.secondaryLabel}</button>`
          : nothing}
      </div>
    </div>
    ${options.steps.length
      ? html`<ol class="k-first-run__journey" aria-label=${options.journeyLabel || 'Getting started'}>
          ${options.steps.map((step, index) => html`<li
            class="k-first-run__step ${index < current ? 'is-complete' : index === current ? 'is-current' : ''}"
            aria-current=${index === current ? 'step' : nothing}
          >
            <span class="k-first-run__marker" aria-hidden="true">
              ${index < current ? icon('check') : index === current ? icon('circle') : index + 1}
            </span>
            <span class="k-first-run__step-copy">
              <span class="k-first-run__step-status">
                ${index < current ? 'Completed step' : index === current ? 'Current step' : 'Upcoming step'}:
              </span>
              <strong>${step.label}</strong><small>${step.description}</small>
            </span>
          </li>`)}
        </ol>`
      : nothing}
  </section>`
}

export interface CreateGuidanceValue {
  label: string
  value: string
  technical?: boolean
}

export interface CreateGuidanceOptions {
  icon: IconName
  title: string
  description: string
  prerequisites?: readonly string[]
  values?: readonly CreateGuidanceValue[]
  valuesHeading?: string
  nextSteps?: readonly string[]
}

export function createGuidance(options: CreateGuidanceOptions): TemplateResult {
  const prerequisites = options.prerequisites || []
  const values = options.values || []
  const nextSteps = options.nextSteps || []
  return html`<aside class="k-create-guidance" aria-label=${options.title}>
    <div class="k-create-guidance__heading">${icon(options.icon)}<h3>${options.title}</h3></div>
    <p class="k-create-guidance__description">${options.description}</p>
    ${prerequisites.length
      ? html`<section class="k-create-guidance__section"><h4>Prerequisites</h4><ul>${prerequisites.map((item) => html`<li>${item}</li>`)}</ul></section>`
      : nothing}
    ${values.length
      ? html`<section class="k-create-guidance__section">
          <h4>${options.valuesHeading || 'What Faros will create'}</h4>
          <dl class="k-create-guidance__values">
            ${values.map((item) => html`<dt>${item.label}</dt><dd>${item.technical ? html`<code>${item.value}</code>` : item.value}</dd>`)}
          </dl>
        </section>`
      : nothing}
    ${nextSteps.length
      ? html`<section class="k-create-guidance__section"><h4>Next steps</h4><ol>${nextSteps.map((item) => html`<li>${item}</li>`)}</ol></section>`
      : nothing}
  </aside>`
}
