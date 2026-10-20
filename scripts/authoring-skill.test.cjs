const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const directory = path.join(__dirname, '../.github/skills/canvas-authoring');
const filename = path.join(directory, 'SKILL.md');

test('Canvas authoring is discoverable as a bounded repository skill', () => {
  assert.ok(fs.existsSync(filename), 'Copilot needs .github/skills/canvas-authoring/SKILL.md');
  const skill = fs.readFileSync(filename, 'utf8');
  const match = skill.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(match, 'skill has YAML frontmatter');
  const fields = Object.fromEntries(match[1].split('\n').map(line => {
    const colon = line.indexOf(':');
    assert.ok(colon > 0, 'frontmatter uses single-line fields');
    return [line.slice(0, colon), line.slice(colon + 1).trim()];
  }));
  assert.equal(fields.name, path.basename(directory));
  assert.match(fields.name, /^[a-z][a-z0-9-]*$/);
  assert.ok(fields.description?.length > 0 && fields.description.length <= 1024);
  assert.deepEqual(Object.keys(fields).sort(), ['description', 'name'], 'only discovery metadata, no tool pre-approval');
  assert.ok(skill.trimEnd().split('\n').length < 100, 'keep the main skill small');
});

test('authoring skill resources resolve within the checkout', () => {
  assert.ok(fs.existsSync(filename), 'repository skill exists');
  const skill = fs.readFileSync(filename, 'utf8');
  const links = [...skill.matchAll(/\]\(([^)]+)\)/g)].map(match => match[1]);
  assert.ok(links.length > 0, 'skill links to maintained authoring references');
  for (const link of links) {
    if (link.startsWith('https://')) continue;
    assert.ok(!path.isAbsolute(link), 'resource links stay portable');
    const target = path.resolve(directory, link.split('#')[0]);
    assert.ok(target.startsWith(path.resolve(__dirname, '..') + path.sep), 'resource stays inside checkout');
    assert.ok(fs.statSync(target).isFile(), link);
  }
});
