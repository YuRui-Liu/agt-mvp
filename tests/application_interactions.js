const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const template = fs.readFileSync('job-application.html.tmpl', 'utf8');
const scripts = [...template.matchAll(/<script>([\s\S]*?)<\/script>/g)];
const source = scripts.at(-1)[1].replaceAll('{{RETURN_URL}}', '/report?token=test');

function element(value = '') {
  const listeners = {};
  return {
    value, textContent: '', disabled: false, files: [],
    classList: { add() {}, remove() {}, toggle() {} },
    addEventListener(type, listener) { listeners[type] = listener; },
    dispatch(type, event = {}) { return listeners[type]({ target: this, ...event }); },
    focus() { this.focused = true; },
  };
}

function setup(ids) {
  const elements = {
    aitiIdInput: element(),
    aitiStatus: element(),
    toast: element(),
    verifyButton: element(),
    resumeInput: element(),
    fileName: element(),
    submitApplication: element(),
    cancelApplication: element(),
    locationSelect: element(),
    name: element(),
    phone: element(),
    email: element(),
  };
  let applications = 0;
  const context = vm.createContext({
    document: { getElementById: (id) => elements[id] },
    AITIMock: {
      getState: () => ({ ids }),
      validateId: (value) => /^(?=.*[A-Z])(?=.*\d)[A-Z0-9]{6}$/.test(value),
      lookupId: (value) => {
        const found = ids.find((item) => item.value === value);
        return !found ? { status: 'missing' } :
          { status: found.associated ? 'associated' : 'available' };
      },
      createApplication: (value) => {
        applications += 1;
        ids.find((item) => item.value === value).associated = true;
      },
    },
    location: { href: '' },
    clearTimeout() {},
    setTimeout() { return 1; },
  });
  context.window = context;
  vm.runInContext(source, context);
  return { elements, context, applications: () => applications };
}

{
  const app = setup([
    { value: 'A1B2C3', associated: false },
    { value: 'D4E5F6', associated: false },
  ]);
  assert.equal(app.elements.aitiIdInput.value, 'D4E5F6');
}

{
  const app = setup([]);
  assert.equal(app.elements.aitiIdInput.value, '');
  app.elements.aitiIdInput.value = 'bad';
  app.elements.verifyButton.dispatch('click');
  assert.match(app.elements.aitiStatus.textContent, /6 位/);
  app.elements.aitiIdInput.value = 'A1B2C3';
  app.elements.verifyButton.dispatch('click');
  assert.match(app.elements.aitiStatus.textContent, /无此ID/);
}

{
  const ids = [{ value: 'A1B2C3', associated: false }];
  const app = setup(ids);
  const submit = app.elements.submitApplication;
  submit.dispatch('click');
  assert.match(app.elements.toast.textContent, /地点/);
  app.elements.locationSelect.value = '北京';
  submit.dispatch('click');
  assert.match(app.elements.toast.textContent, /简历/);
  app.elements.resumeInput.files = [{ name: '<img src=x onerror=alert(1)>.pdf' }];
  app.elements.resumeInput.dispatch('change');
  assert.equal(app.elements.fileName.textContent, '已选择：<img src=x onerror=alert(1)>.pdf');
  submit.dispatch('click');
  assert.match(app.elements.toast.textContent, /姓名/);
  app.elements.name.value = '<img src=x onerror=alert(1)>';
  app.elements.phone.value = '13800138000';
  app.elements.email.value = 'test@example.com';
  submit.dispatch('click');
  assert.equal(app.applications(), 1);
  assert.equal(submit.disabled, true);
  assert.equal(submit.textContent, '已完成投递');
  submit.dispatch('click');
  assert.equal(app.applications(), 1);
  assert.equal('innerHTML' in app.elements.fileName, false);
}

console.log('application interaction tests passed');
