#!/usr/bin/env python3
"""The third-party notices list each installed package instance a web bundle
holds, nested versions included, and keep a licence a package states only in
its README."""
import importlib.util
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("tpn", os.path.join(HERE, "third-party-notices.py"))
tpn = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tpn)


def package(d, name, version, deps=None, licence=None, readme=None):
    os.makedirs(d)
    with open(os.path.join(d, "package.json"), "w") as f:
        json.dump({"name": name, "version": version, "dependencies": deps or {}}, f)
    if licence:
        with open(os.path.join(d, "LICENSE"), "w") as f:
            f.write(licence)
    if readme:
        with open(os.path.join(d, "README.md"), "w") as f:
            f.write(readme)


class WebPackages(unittest.TestCase):
    def setUp(self):
        self.web = tempfile.mkdtemp()
        self.addCleanup(__import__("shutil").rmtree, self.web)
        with open(os.path.join(self.web, "package.json"), "w") as f:
            json.dump({"dependencies": {"app": "1", "shared": "3"}}, f)
        nm = os.path.join(self.web, "node_modules")
        package(os.path.join(nm, "app"), "app", "1.0.0", {"shared": "2"}, "app licence")
        package(os.path.join(nm, "shared"), "shared", "3.0.0", licence="ISC, shared 3")
        package(os.path.join(nm, "app", "node_modules", "shared"), "shared", "2.0.0", licence="BSD, shared 2")
        package(os.path.join(nm, "readmeonly"), "readmeonly", "1.0.0",
                readme="# readmeonly\n\nUse it.\n\n## License\n\n(The MIT License)\n\nCopyright (c) 2016 Someone\n\nPermission is hereby granted.\n\n## Thanks\n\nnot licence\n")

    def test_a_nested_version_is_listed_as_the_importer_bundles_it(self):
        got = {(n, v) for n, v, _ in tpn.web_packages(self.web)}
        self.assertEqual(got, {("app", "1.0.0"), ("shared", "3.0.0"), ("shared", "2.0.0")})

    def test_a_licence_kept_in_a_readme_is_kept_verbatim(self):
        name, section = tpn.readme_licence(os.path.join(self.web, "node_modules", "readmeonly"))
        self.assertEqual(name, "README.md")
        self.assertIn("Copyright (c) 2016 Someone", section)
        self.assertIn("Permission is hereby granted.", section)
        self.assertNotIn("not licence", section)


if __name__ == "__main__":
    unittest.main()
