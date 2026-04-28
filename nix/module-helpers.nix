# Module generation helpers
# Provides mkSimplePackageModule, mkEnableablePackageModule, mkDockRegistration, mkProgramModule
_: {
  # Creates a simple module that just installs a package
  # Usage: mkSimplePackageModule "nixd" pkgs.nixd
  mkSimplePackageModule = _name: package: _: {
    home.packages = [ package ];
  };

  # Creates a module with an enable option and package installation
  # Usage: mkEnableablePackageModule "phillipgreenii.programs.myapp" "My Application" pkgs.myapp
  mkEnableablePackageModule =
    optionPath: description: package:
    { lib, config, ... }:
    let
      cfg = lib.attrsets.getAttrFromPath (lib.splitString "." optionPath) config;
    in
    {
      options = lib.setAttrByPath (lib.splitString "." optionPath) {
        enable = lib.mkOption {
          type = lib.types.bool;
          default = true;
          description = "Enable ${description}";
        };
      };

      config = lib.mkIf cfg.enable { home.packages = [ package ]; };
    };

  # Creates a dock app registration configuration
  # Usage: mkDockRegistration lib config "phillipgreenii.programs.ghostty.enablePersistentDockApp" "/Applications/Ghostty.app"
  mkDockRegistration =
    lib: config: enableOptionPath: appPath:
    let
      enableOption = lib.attrsets.getAttrFromPath (lib.splitString "." enableOptionPath) config;
    in
    lib.mkIf enableOption [ appPath ];

  # Creates a complete program module with enable option and optional dock registration
  # Usage: mkProgramModule {
  #   optionPath = "phillipgreenii.programs.myapp";
  #   description = "My Application";
  #   package = pkgs.myapp;
  #   enableByDefault = true;
  #   dockApp = "${pkgs.myapp}/Applications/MyApp.app";  # optional
  #   extraConfig = { ... };  # optional
  # }
  mkProgramModule =
    {
      optionPath,
      description,
      package,
      enableByDefault ? true,
      dockApp ? null,
      extraConfig ? { },
    }:
    { lib, config, ... }:
    let
      cfg = lib.attrsets.getAttrFromPath (lib.splitString "." optionPath) config;
    in
    {
      options = lib.setAttrByPath (lib.splitString "." optionPath) (
        {
          enable = lib.mkOption {
            type = lib.types.bool;
            default = enableByDefault;
            description = "Enable ${description}";
          };
        }
        // lib.optionalAttrs (dockApp != null) {
          enablePersistentDockApp = lib.mkOption {
            type = lib.types.bool;
            default = true;
            description = "Add ${description} to the macOS dock as a persistent app";
          };
        }
      );

      config = lib.mkIf cfg.enable (
        lib.mkMerge [
          { home.packages = [ package ]; }
          (lib.mkIf (dockApp != null && cfg.enablePersistentDockApp or false) {
            phillipgreenii.darwin.system.persistentDockApps = [ dockApp ];
          })
          extraConfig
        ]
      );
    };
}
