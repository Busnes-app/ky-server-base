"""A scaffold must have its own recovery identity or fail before creating files."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class ScaffoldIdentityTests(unittest.TestCase):
    def test_product_default_and_changed_template(self):
        with tempfile.TemporaryDirectory(prefix="ky-init-check-") as temp:
            source = Path(temp) / "base"
            (source / "scripts").mkdir(parents=True)
            shutil.copyfile(ROOT / "scripts/ky-init.sh", source / "scripts/ky-init.sh")
            shutil.copyfile(ROOT / "docker-compose.yml", source / "docker-compose.yml")
            (source / "go.mod").write_text("module example.test/base\n\ngo 1.26\n")
            (source / "main.go").write_text("package main\n\nfunc main() {}\n")
            environment = dict(os.environ, PUSH_RELAY_URL="", APNS_RELAY_URL="")
            target = Path(temp) / "product"
            command = ["bash", str(source / "scripts/ky-init.sh"), "KyScaffold", str(target)]
            result = subprocess.run(command, env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("KY_APP_NAME=${KY_APP_NAME:-KyScaffold}",
                          (target / "docker-compose.yml").read_text())
            self.assertIn("module github.com/Busnes-app/KyScaffold",
                          (target / "go.mod").read_text())

            derived = Path(temp) / "second-product"
            result = subprocess.run(
                ["bash", str(target / "scripts/ky-init.sh"), "SecondProduct", str(derived)],
                env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("KY_APP_NAME=${KY_APP_NAME:-SecondProduct}",
                          (derived / "docker-compose.yml").read_text())

            compose = source / "docker-compose.yml"
            compose.write_text(compose.read_text().replace("KY_APP_NAME=", "KY_RENAMED_APP_NAME="))
            missing_target = Path(temp) / "missing-template"
            command[-1] = str(missing_target)
            result = subprocess.run(command, env=environment, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn("KY_APP_NAME template", result.stdout + result.stderr)
            self.assertFalse(missing_target.exists(), "changed template created a misleading scaffold")

            for name in ("bad/name", "with space", "bad..name"):
                with self.subTest(name=name):
                    command[2] = name
                    result = subprocess.run(command, env=environment, capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn("service label", result.stderr)
                    self.assertFalse(missing_target.exists())


if __name__ == "__main__":
    unittest.main()
