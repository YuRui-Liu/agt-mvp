const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const modulePath = require.resolve('../aiti-mock.js');
delete globalThis.AITIMock;
delete require.cache[modulePath];
const { createMock } = require(modulePath);

assert.equal(globalThis.AITIMock, undefined);

function safeRandom(fill = 7) {
  return (length) => new Uint8Array(length).fill(fill);
}

function storage(initial) {
  const values = new Map(initial);
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: (key) => values.delete(key),
    values,
  };
}

assert.equal(typeof createMock, 'function');

{
  const source = fs.readFileSync(require.resolve('../aiti-mock.js'), 'utf8');
  const browser = vm.createContext({});
  vm.runInContext(
    "Object.defineProperty(globalThis, 'localStorage', { get() { throw new Error('storage denied'); } })",
    browser,
  );
  assert.doesNotThrow(() => vm.runInContext(source, browser));
  assert.equal(typeof browser.AITIMock.generateId, 'function');
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom() });
  // Local demo rule: any non-empty, digits-only code is accepted.
  assert.equal(mock.verifyCode('888888'), true);
  assert.equal(mock.verifyCode(' 42 '), true);
  assert.equal(mock.verifyCode(''), false);
  assert.equal(mock.verifyCode('12a'), false);

  assert.equal(mock.validateId('a1b2c3'), true);
  assert.equal(mock.validateId('ABCDEF'), false);
  assert.equal(mock.validateId('123456'), false);
  assert.equal(mock.validateId('A1B2C!'), false);
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom() });
  assert.throws(() => mock.generateId(''), /手机号格式不正确/);
  assert.throws(() => mock.generateId('1380013800'), /手机号格式不正确/);
  assert.throws(() => mock.generateId('23800138000'), /手机号格式不正确/);
  assert.throws(() => mock.generateId('138 0013 8000'), /手机号格式不正确/);

  const generated = mock.generateId('13800138000');
  assert.match(generated.value, /^(?=.*[A-Z])(?=.*\d)[A-Z0-9]{6}$/);
  assert.equal(generated.aitiId, generated.value);
  assert.equal(generated.phone, '138****8000');
  assert.equal(mock.lookupId(generated.value.toLowerCase()).status, 'available');
}

{
  const persistentStorage = storage();
  const first = createMock({ storage: persistentStorage, randomBytes: safeRandom() });
  const generated = first.generateId('13600136000');
  const second = createMock({ storage: persistentStorage, randomBytes: safeRandom() });
  assert.equal(second.lookupId(generated.value).status, 'available');

  const stateCopy = second.getState();
  stateCopy.ids[0].associated = !stateCopy.ids[0].associated;
  stateCopy.applications.push({ aitiId: 'MUTATE' });
  assert.notDeepEqual(second.getState(), stateCopy);
}

{
  const broken = storage([['aiti-demo-state-v1', '{not json']]);
  const mock = createMock({ storage: broken, randomBytes: safeRandom() });
  assert.equal(Array.isArray(mock.getState().ids), true);
  assert.doesNotThrow(() => mock.generateId('13700137000'));

  const structurallyBroken = storage([[
    'aiti-demo-state-v1',
    JSON.stringify({ profile: {}, ids: [null], applications: [] }),
  ]]);
  const recovered = createMock({ storage: structurallyBroken, randomBytes: safeRandom() });
  assert.doesNotThrow(() => recovered.lookupId('A7TI8P'));
  assert.equal(recovered.lookupId('A7TI8P').status, 'available');

  const unavailable = {
    getItem() { throw new Error('denied'); },
    setItem() { throw new Error('denied'); },
    removeItem() { throw new Error('denied'); },
  };
  const memoryMock = createMock({ storage: unavailable, randomBytes: safeRandom() });
  const generated = memoryMock.generateId('13500135000');
  assert.equal(memoryMock.lookupId(generated.value).status, 'available');
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom() });
  assert.deepEqual(mock.lookupId('NOPE1'), { status: 'missing' });
  const generated = mock.generateId('13400134000');
  const associated = mock.associateId(generated.value);
  assert.equal(associated.associated, true);
  assert.throws(
    () => mock.associateId(generated.value),
    /当前已经关联过其他应聘记录，请更换后重新提交/,
  );
  assert.throws(
    () => mock.associateId('NOPE1'),
    /当前无此ID，请生成唯一ID后提交！/,
  );
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom() });
  const generated = mock.generateId('13300133000');
  const application = mock.createApplication(generated.value, '前端工程师');
  assert.equal(application.aitiId, generated.value);
  assert.equal(application.jobName, '前端工程师');
  assert.equal('kwaitiId' in application, false);
  assert.equal(mock.lookupId(generated.value).status, 'associated');
  assert.throws(
    () => mock.createApplication(generated.value, '前端工程师'),
    /当前已经关联过其他应聘记录，请更换后重新提交/,
  );
  assert.equal(mock.getState().applications.length, 1);
}

{
  const persistentStorage = storage();
  const mock = createMock({ storage: persistentStorage, randomBytes: safeRandom() });
  mock.generateId('13200132000');
  mock.reset();
  assert.equal(mock.getState().applications.length, 0);
  assert.equal(
    createMock({ storage: persistentStorage, randomBytes: safeRandom() }).getState().applications.length,
    0,
  );
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom(0) });
  mock.generateId('13100131000');
  assert.throws(
    () => mock.generateId('13100131001'),
    /无法安全生成唯一 AITI ID/,
  );
}

{
  const corruptStates = [
    {
      profile: { score: 88 },
      ids: [
        { value: 'A1B2C3', aitiId: 'A1B2C3', phone: '138****0000', associated: false },
        { value: 'A1B2C3', aitiId: 'A1B2C3', phone: '139****0000', associated: false },
      ],
      applications: [],
    },
    {
      profile: { score: 88 },
      ids: [{ value: 'A1B2C3', aitiId: 'A1B2C3', phone: '138****0000', associated: false }],
      applications: [null],
    },
    {
      profile: { score: 88 },
      ids: [{ value: 'A1B2C3', aitiId: 'A1B2C3', phone: '138****0000', associated: false }],
      applications: [{ aitiId: 'Z9Y8X7', jobName: 'Z', score: 88, level: '赋能级' }],
    },
    {
      profile: { score: 88 },
      ids: [{ value: 'A1B2C3', aitiId: 'A1B2C3', phone: '138****0000', associated: true }],
      applications: [
        { aitiId: 'A1B2C3', jobName: 'Z', score: 88, level: '赋能级' },
        { aitiId: 'A1B2C3', jobName: 'Y', score: 88, level: '赋能级' },
      ],
    },
  ];
  for (const corruptState of corruptStates) {
    const saved = storage([['aiti-demo-state-v1', JSON.stringify(corruptState)]]);
    const recovered = createMock({ storage: saved, randomBytes: safeRandom() });
    assert.equal(recovered.lookupId('A7TI8P').status, 'available');
  }
}

{
  const mock = createMock({ storage: storage(), randomBytes: safeRandom() });
  assert.equal(mock.maskPhone(null), '未绑定手机号');
  assert.equal(mock.maskPhone({}), '未绑定手机号');
  assert.equal(mock.maskPhone('123'), '未绑定手机号');
  assert.equal(mock.maskPhone('13800138000'), '138****8000');
}

console.log('AITI mock core tests passed');
