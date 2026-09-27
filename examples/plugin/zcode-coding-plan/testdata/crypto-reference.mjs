import { readFile } from 'node:fs/promises';
import {
  createHash,
  createHmac,
  createPrivateKey,
  createPublicKey,
  hkdfSync,
  sign,
  verify,
} from 'node:crypto';

// 仅供合成差分测试；没有网络、账号发现或产品运行时入口。
const vector = JSON.parse(await readFile(new URL('./crypto-vector.json', import.meta.url)));
const [id, secret] = vector.api_key.split('.');
const derive = (info) => Buffer.from(hkdfSync('sha256', secret, 'WD_CLIENT_SIGN_KDF_SALT', info, 32));
const privateKey = createPrivateKey({
  key: Buffer.from(`302e020100300506032b657004220420${vector.seed_hex}`, 'hex'),
  format: 'der',
  type: 'pkcs8',
});
const publicKey = createPublicKey(privateKey);
if (!publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).equals(Buffer.from(vector.public_hex, 'hex'))) {
  throw new Error('RFC public key mismatch');
}
const message = Buffer.from(`${id}\n${vector.timestamp}\n3.14.3\n${vector.session_id}\n${vector.nonce}`);
const signature = sign(null, message, privateKey);
if (!verify(null, message, publicKey, signature)) throw new Error('Signature verification failed');
const bytes = createHash('sha256').update(JSON.stringify(vector.scopes)).digest().subarray(0, 16);
bytes[6] = (bytes[6] & 15) | 128;
bytes[8] = (bytes[8] & 63) | 128;
const hex = bytes.toString('hex');
process.stdout.write(JSON.stringify({
  hmac_key: derive('getSignKey_hmac').toString('hex'),
  ed_key: derive('ed25519_priv').toString('hex'),
  handshake: {
    apiKey: vector.api_key,
    nonce: vector.nonce,
    sig: createHmac('sha256', derive('getSignKey_hmac'))
      .update(`get_sign_key\n${id}\n${vector.timestamp}\n${vector.nonce}`).digest('base64'),
    ts: vector.timestamp,
  },
  signature: signature.toString('base64'),
  session: `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`,
}));
