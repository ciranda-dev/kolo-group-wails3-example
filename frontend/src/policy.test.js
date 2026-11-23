import test from 'node:test'
import assert from 'node:assert/strict'
import { policyForm, policyProblem, policyToSend, samePolicy, withPoster } from './policy.js'

test('a policy from Go gets every field in place', () => {
  assert.deepEqual(policyForm(null), { history: 'all', since: 0, posters: 'all', persons: [], readLink: false })
  assert.deepEqual(policyForm({ history: 'bad', posters: 'bad', persons: null }), policyForm({}))
  assert.deepEqual(policyForm({ history: 'since', since: 4, posters: 'chosen', persons: ['p'], readLink: true }), { history: 'since', since: 4, posters: 'chosen', persons: ['p'], readLink: true })
})

test('a read link needs posters and chosen posters need a person', () => {
  assert.equal(policyProblem(policyForm({ readLink: true })), 'policy.linkNeedsPosters')
  assert.equal(policyProblem(policyForm({ readLink: true, posters: 'admins' })), '')
  assert.equal(policyProblem(policyForm({ posters: 'chosen' })), 'policy.chosenEmpty')
  assert.equal(policyProblem(policyForm({ posters: 'chosen', persons: ['p'], readLink: true })), '')
  assert.equal(policyProblem(policyForm({})), '')
})

test('letting a person post makes the posters chosen, and taking the last one out says so', () => {
  const form = withPoster(policyForm({}), 'p1', true)
  assert.deepEqual([form.posters, form.persons], ['chosen', ['p1']])
  assert.deepEqual(withPoster(form, 'p2', true).persons, ['p1', 'p2'])
  assert.deepEqual(withPoster(withPoster(form, 'p1', true), 'p1', true).persons, ['p1'])
  assert.equal(policyProblem(withPoster(form, 'p1', false)), 'policy.chosenEmpty')
})

test('the epoch is sent only with "since"', () => {
  assert.equal(policyToSend(policyForm({ history: 'all', since: 7 })).since, 0)
  assert.equal(policyToSend(policyForm({ history: 'since', since: 7 })).since, 7)
})

test('two forms are the same when they say the same', () => {
  const a = policyForm({ posters: 'chosen', persons: ['x', 'y'] })
  assert.ok(samePolicy(a, policyForm({ posters: 'chosen', persons: ['y', 'x'] })))
  assert.ok(!samePolicy(a, policyForm({ posters: 'chosen', persons: ['x'] })))
  assert.ok(!samePolicy(a, { ...a, readLink: true }))
})
