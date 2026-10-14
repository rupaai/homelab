// Draws the home-screen icons (a house on an indigo square) as plain PNGs. No dependencies.
import { writeFileSync } from 'node:fs';
import { deflateSync } from 'node:zlib';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const out = join(dirname(fileURLToPath(import.meta.url)), '..', 'static');

const crcTable = Array.from({ length: 256 }, (_, n) => {
	let c = n;
	for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
	return c >>> 0;
});
const crc = (buf) => {
	let c = 0xffffffff;
	for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
	return (c ^ 0xffffffff) >>> 0;
};
const chunk = (type, data) => {
	const len = Buffer.alloc(4);
	len.writeUInt32BE(data.length);
	const td = Buffer.concat([Buffer.from(type), data]);
	const sum = Buffer.alloc(4);
	sum.writeUInt32BE(crc(td));
	return Buffer.concat([len, td, sum]);
};

// Inside a house shape? Coordinates are 0..1.
function house(x, y) {
	const roof = y >= 0.22 && y <= 0.5 && Math.abs(x - 0.5) <= (y - 0.22) * 1.45;
	const body = x >= 0.27 && x <= 0.73 && y > 0.46 && y <= 0.78;
	const door = x >= 0.44 && x <= 0.56 && y >= 0.6 && y <= 0.78;
	return (roof || body) && !door;
}

function png(size) {
	const bg = [0x3b, 0x5b, 0xdb];
	const raw = Buffer.alloc((size * 4 + 1) * size);
	const ss = 3; // supersampling for smooth edges
	for (let py = 0; py < size; py++) {
		raw[py * (size * 4 + 1)] = 0;
		for (let px = 0; px < size; px++) {
			let hit = 0;
			for (let sy = 0; sy < ss; sy++)
				for (let sx = 0; sx < ss; sx++)
					if (house((px + (sx + 0.5) / ss) / size, (py + (sy + 0.5) / ss) / size)) hit++;
			const a = hit / (ss * ss);
			const o = py * (size * 4 + 1) + 1 + px * 4;
			for (let i = 0; i < 3; i++) raw[o + i] = Math.round(bg[i] * (1 - a) + 255 * a);
			raw[o + 3] = 255;
		}
	}
	const ihdr = Buffer.alloc(13);
	ihdr.writeUInt32BE(size, 0);
	ihdr.writeUInt32BE(size, 4);
	ihdr[8] = 8;
	ihdr[9] = 6;
	return Buffer.concat([
		Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
		chunk('IHDR', ihdr),
		chunk('IDAT', deflateSync(raw)),
		chunk('IEND', Buffer.alloc(0))
	]);
}

writeFileSync(join(out, 'apple-touch-icon.png'), png(180));
writeFileSync(join(out, 'icon-512.png'), png(512));
console.log('icons written');
