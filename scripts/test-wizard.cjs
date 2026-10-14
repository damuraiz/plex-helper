// Check the JSONP module contract used by DSM, against the packaged wizard.
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const [entry] = JSON.parse(fs.readFileSync(0, 'utf8'));
let component, disabled, stateCalls = 0;
const owner = { getButton: name => {
  assert.equal(name, 'next');
  return {setDisabled: value => {disabled = value;}};
}};
const useHook = props => ({getNext: () => 'apply', checkState: target => {
  assert.equal(target, owner);
  stateCalls++;
  disabled = false;
}});
useHook.props = {getOwner: Function};
const context = {
  Vue: {ref: value => ({value}), watchEffect: fn => fn()},
  SYNO: {SDS: {PkgManApp: {Custom: {useHook, JsonpLoader: {load: module => {
    // Synology's webpack JSONP entry exports its component as default.
    assert.equal(module.default.name, entry.custom_render_name);
    component = module.default.render;
  }}}}}}
};
vm.runInNewContext(entry.custom_render_fn, context, {timeout: 1000});
const state = component.setup({getOwner: () => owner});
assert.equal(disabled, true);
state.password.value = 'short';
state.checkState(owner);
assert.equal(disabled, true);
state.password.value = 'wizard-contract-password';
state.checkState();
assert.equal(disabled, false);
assert.equal(state.getValues()[0].wizard_admin_password, state.password.value);
assert.equal(state.getNext(), 'apply');
assert.equal(stateCalls, 3);
const rendered = component.render.call(state, (tag, ...args) => ({tag, args}));
assert.equal(rendered.tag, 'pkg-center-step-content');
console.log('PASS: wizard JSONP default export, setup, password validation, submission');
