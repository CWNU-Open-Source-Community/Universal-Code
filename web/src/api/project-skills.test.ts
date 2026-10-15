import { validProjectSkillChoice, validSkillSelectionRef } from "./project-skills";

const pin = { installation_id: "native-install", package_id: "package-" + "p".repeat(120), component_id: "skill:native",
  revision: "a".repeat(64), installation_generation: 3 };

it("retains exact native read references and legacy refs without inventing an author version", () => {
  const ref = JSON.stringify(pin);
  expect(ref.length).toBeGreaterThan(128);
  expect(validProjectSkillChoice({ refs: [ref, "external@1.0.0"], confirmUntrustedContext: true, source: "installed" })).toBe(true);
  for (const invalid of [JSON.stringify({ ...pin, resource: "assets/file" }), JSON.stringify({ ...pin, catalog: true }),
    JSON.stringify({ ...pin, installation_generation: 0 }), JSON.stringify({ ...pin, revision: "newest" }),
    JSON.stringify({ ...pin, component_id: " skill:native" }), ` ${ref}`, ref.replace("{", '{"extra":true,'),
    JSON.stringify(Object.fromEntries(Object.entries(pin).reverse()))]) expect(validSkillSelectionRef(invalid)).toBe(false);
});
