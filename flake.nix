{
  description = "Polis autonomous-agent fleet coordination kernel";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      pkgsFor = system: import nixpkgs { inherit system; };
      polisFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        pkgs.python313Packages.buildPythonApplication {
          pname = "polis-agent-fleet";
          version = "0.1.0";
          pyproject = true;
          src = self;

          build-system = [ pkgs.python313Packages.setuptools ];
          nativeCheckInputs = [ pkgs.python313Packages.pytestCheckHook ];
          pythonImportsCheck = [ "polis" ];

          preCheck = ''
            export HOME="$TMPDIR"
          '';
        };
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          polis = polisFor system;
        in
        {
          default = polis;
          container = pkgs.dockerTools.buildLayeredImage {
            name = "polis";
            tag = "dev";
            contents = [
              polis
              pkgs.cacert
            ];
            config = {
              Cmd = [ "/bin/polis-controller" ];
              Env = [
                "PATH=/bin"
                "POLIS_DB_PATH=/data/polis.db"
                "PYTHONUNBUFFERED=1"
              ];
              ExposedPorts."8080/tcp" = { };
              User = "10001:10001";
              WorkingDir = "/";
            };
          };
        }
      );

      checks = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          package = polisFor system;
          lint = pkgs.runCommand "polis-lint" { nativeBuildInputs = [ pkgs.ruff ]; } ''
            cd ${self}
            ruff check --no-cache src tests
            ruff format --no-cache --check src tests
            touch $out
          '';
        }
      );

      apps = forAllSystems (
        system:
        let
          polis = polisFor system;
        in
        {
          default = {
            type = "app";
            program = "${polis}/bin/polis-controller";
            meta.description = "Run the Polis controller";
          };
          controller = {
            type = "app";
            program = "${polis}/bin/polis-controller";
            meta.description = "Run the Polis controller";
          };
          runner = {
            type = "app";
            program = "${polis}/bin/polis-runner";
            meta.description = "Run a Polis task runner";
          };
          cli = {
            type = "app";
            program = "${polis}/bin/polis";
            meta.description = "Control a Polis fleet";
          };
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = pkgs.mkShell {
            packages = with pkgs; [
              curl
              gnumake
              jq
              python313
              ruff
              sqlite
            ];
            shellHook = ''
              export PYTHONPATH="$PWD/src''${PYTHONPATH:+:$PYTHONPATH}"
            '';
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
