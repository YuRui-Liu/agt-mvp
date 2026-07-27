(function (root, factory) {
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { createMock: factory };
  }
  if (root) {
    let browserStorage;
    try {
      browserStorage = root.localStorage;
    } catch (_error) {
      browserStorage = undefined;
    }
    root.AITIMock = factory({ storage: browserStorage });
  }
})(typeof globalThis === 'undefined' ? this : globalThis, function createMock(options) {
  'use strict';

  const storage = options && options.storage;
  const storageKey = 'aiti-demo-state-v1';
  const defaultState = {
    profile: { typeCode: 'CPQL', title: '精密系统设计师', score: 88 },
    ids: [
      { value: 'A7TI8P', aitiId: 'A7TI8P', phone: '138****0000', associated: false },
      { value: 'I86AT2', aitiId: 'I86AT2', phone: '139****0000', associated: true },
    ],
    applications: [],
  };

  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function validState(value) {
    return Boolean(
      value
      && typeof value === 'object'
      && value.profile
      && typeof value.profile === 'object'
      && Number.isFinite(value.profile.score)
      && Array.isArray(value.ids)
      && value.ids.every((item) => (
        item
        && typeof item === 'object'
        && /^[A-Z0-9]{6}$/.test(item.value)
        && /[A-Z]/.test(item.value)
        && /[0-9]/.test(item.value)
        && item.aitiId === item.value
        && typeof item.phone === 'string'
        && typeof item.associated === 'boolean'
      ))
      && Array.isArray(value.applications),
    );
  }

  function readState() {
    if (!storage || typeof storage.getItem !== 'function') {
      return clone(defaultState);
    }
    try {
      const saved = storage.getItem(storageKey);
      if (!saved) return clone(defaultState);
      const parsed = JSON.parse(saved);
      return validState(parsed) ? clone(parsed) : clone(defaultState);
    } catch (_error) {
      return clone(defaultState);
    }
  }

  let state = readState();

  function saveState() {
    if (storage && typeof storage.setItem === 'function') {
      try {
        storage.setItem(storageKey, JSON.stringify(state));
      } catch (_error) {
        // Storage can be disabled or full; the in-memory state remains usable.
      }
    }
    return clone(state);
  }

  function normalize(value) {
    return String(value || '').trim().toUpperCase();
  }

  function validateId(value) {
    const normalized = normalize(value);
    return (
      /^[A-Z0-9]{6}$/.test(normalized)
      && /[A-Z]/.test(normalized)
      && /[0-9]/.test(normalized)
    );
  }

  function lookupId(value) {
    const item = state.ids.find((entry) => entry.value === normalize(value));
    if (!item) return { status: 'missing' };
    return {
      status: item.associated ? 'associated' : 'available',
      item: clone(item),
    };
  }

  function verifyCode(value) {
    // Local demo only: every non-empty, digits-only code is accepted.
    return /^\d+$/.test(String(value || '').trim());
  }

  function maskPhone(phone) {
    return `${phone.slice(0, 3)}****${phone.slice(-4)}`;
  }

  function nextId() {
    const letters = 'ABCDEFGHJKLMNPQRSTUVWXYZ';
    const chars = `${letters}0123456789`;
    let value;
    do {
      const letter = letters[Math.floor(Math.random() * letters.length)];
      const digit = String(Math.floor(Math.random() * 10));
      const tail = Array.from(
        { length: 4 },
        () => chars[Math.floor(Math.random() * chars.length)],
      ).join('');
      value = `${letter}${digit}${tail}`;
    } while (state.ids.some((entry) => entry.value === value));
    return value;
  }

  function generateId(phone) {
    const normalizedPhone = String(phone || '');
    if (!/^1[3-9]\d{9}$/.test(normalizedPhone)) {
      throw new Error('手机号格式不正确');
    }
    const value = nextId();
    const item = {
      value,
      aitiId: value,
      phone: maskPhone(normalizedPhone),
      associated: false,
    };
    state.ids.push(item);
    saveState();
    return clone(item);
  }

  function levelForScore(score) {
    if (score >= 90) return '构建级';
    if (score >= 80) return '赋能级';
    if (score >= 65) return '熟练级';
    if (score >= 40) return '入门级';
    return '起步级';
  }

  function associationError(status) {
    return new Error(
      status === 'missing'
        ? '当前无此ID，请生成唯一ID后提交！'
        : '当前已经关联过其他应聘记录，请更换后重新提交',
    );
  }

  function associateId(value) {
    const normalized = normalize(value);
    const result = lookupId(normalized);
    if (result.status !== 'available') throw associationError(result.status);
    const entry = state.ids.find((item) => item.value === normalized);
    entry.associated = true;
    saveState();
    return clone(entry);
  }

  function createApplication(value, jobName) {
    const normalized = normalize(value);
    const result = lookupId(normalized);
    if (result.status !== 'available') throw associationError(result.status);
    const score = state.profile.score;
    const application = {
      aitiId: normalized,
      jobName: jobName || 'Z',
      score,
      level: levelForScore(score),
    };
    const entry = state.ids.find((item) => item.value === normalized);
    entry.associated = true;
    state.applications.push(application);
    saveState();
    return clone(application);
  }

  function reset() {
    state = clone(defaultState);
    return saveState();
  }

  return {
    getState: () => clone(state),
    saveState,
    verifyCode,
    generateId,
    validateId,
    lookupId,
    associateId,
    createApplication,
    maskPhone,
    levelForScore,
    reset,
  };
});
