(function (root, factory) {
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { createMock: factory };
  } else if (root) {
    let browserStorage;
    let browserCrypto;
    try {
      browserStorage = root.localStorage;
    } catch (_error) {
      browserStorage = undefined;
    }
    try {
      browserCrypto = root.crypto;
    } catch (_error) {
      browserCrypto = undefined;
    }
    root.AITIMock = factory({ storage: browserStorage, crypto: browserCrypto });
  }
})(typeof globalThis === 'undefined' ? this : globalThis, function createMock(options) {
  'use strict';

  const storage = options && options.storage;
  const injectedRandomBytes = options && options.randomBytes;
  let cryptoSource = options && options.crypto;
  if (!cryptoSource) {
    try {
      cryptoSource = typeof globalThis !== 'undefined' ? globalThis.crypto : undefined;
    } catch (_error) {
      cryptoSource = undefined;
    }
  }
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
    if (!(
      value && typeof value === 'object'
      && value.profile && typeof value.profile === 'object'
      && Number.isFinite(value.profile.score)
      && Array.isArray(value.ids) && Array.isArray(value.applications)
    )) return false;

    const validIds = value.ids.every((item) => (
        item
        && typeof item === 'object'
        && /^[A-Z0-9]{6}$/.test(item.value)
        && /[A-Z]/.test(item.value)
        && /[0-9]/.test(item.value)
        && item.aitiId === item.value
        && typeof item.phone === 'string'
        && typeof item.associated === 'boolean'
    ));
    if (!validIds) return false;

    const idsByValue = new Map(value.ids.map((item) => [item.value, item]));
    if (idsByValue.size !== value.ids.length) return false;

    const applicationIds = new Set();
    const validApplications = value.applications.every((application) => {
      if (!(
        application
        && typeof application === 'object'
        && validateId(application.aitiId)
        && typeof application.jobName === 'string'
        && application.jobName.length > 0
        && Number.isFinite(application.score)
        && typeof application.level === 'string'
        && application.level.length > 0
      )) return false;
      const linkedId = idsByValue.get(application.aitiId);
      if (!linkedId || !linkedId.associated || applicationIds.has(application.aitiId)) {
        return false;
      }
      applicationIds.add(application.aitiId);
      return true;
    });
    return validApplications;
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
    let digits;
    try {
      digits = String(phone || '').replace(/\D/g, '');
    } catch (_error) {
      return '未绑定手机号';
    }
    return digits.length === 11
      ? `${digits.slice(0, 3)}****${digits.slice(-4)}`
      : '未绑定手机号';
  }

  function randomBytes(length) {
    let bytes;
    if (typeof injectedRandomBytes === 'function') {
      bytes = injectedRandomBytes(length);
    } else if (cryptoSource && typeof cryptoSource.getRandomValues === 'function') {
      bytes = cryptoSource.getRandomValues(new Uint8Array(length));
    } else {
      throw new Error('无法安全生成唯一 AITI ID：安全随机源不可用');
    }
    if (!bytes || bytes.length !== length) {
      throw new Error('无法安全生成唯一 AITI ID：安全随机源返回无效数据');
    }
    return bytes;
  }

  function nextId() {
    const letters = 'ABCDEFGHJKLMNPQRSTUVWXYZ';
    const chars = `${letters}0123456789`;
    const maxAttempts = 32;
    for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
      const bytes = randomBytes(6);
      const tail = Array.from(
        bytes.slice(2),
        (byte) => chars[byte % chars.length],
      ).join('');
      const value = `${letters[bytes[0] % letters.length]}${bytes[1] % 10}${tail}`;
      if (!state.ids.some((entry) => entry.value === value)) return value;
    }
    throw new Error('无法安全生成唯一 AITI ID：随机 ID 连续碰撞');
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
